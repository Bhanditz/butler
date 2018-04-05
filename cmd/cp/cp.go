package cp

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/itchio/wharf/eos/option"

	humanize "github.com/dustin/go-humanize"
	"github.com/itchio/butler/cmd/dl"
	"github.com/itchio/butler/comm"
	"github.com/itchio/butler/mansion"
	"github.com/itchio/httpkit/httpfile"
	"github.com/itchio/httpkit/retrycontext"
	"github.com/itchio/wharf/counter"
	"github.com/itchio/wharf/eos"
	"github.com/itchio/wharf/state"
	"github.com/pkg/errors"
)

type OnCopyStart func(initialProgress float64, totalBytes int64)
type OnCopyStop func()

type CopyParams struct {
	OnStart  OnCopyStart
	OnStop   OnCopyStop
	Consumer *state.Consumer
}

var args = struct {
	src    *string
	dest   *string
	resume *bool
}{}

func Register(ctx *mansion.Context) {
	cmd := ctx.App.Command("cp", "Copy src to dest").Hidden()
	args.src = cmd.Arg("src", "File to read from").Required().String()
	args.dest = cmd.Arg("dest", "File to write to").Required().String()
	args.resume = cmd.Flag("resume", "Try to resume if dest is partially written (doesn't check existing data)").Bool()
	ctx.Register(cmd, do)
}

func do(ctx *mansion.Context) {
	params := &CopyParams{
		OnStart: func(initialProgress float64, totalBytes int64) {
			comm.Progress(initialProgress)
			comm.StartProgressWithTotalBytes(totalBytes)
		},
		OnStop: func() {
			comm.EndProgress()
		},
		Consumer: comm.NewStateConsumer(),
	}

	ctx.Must(Do(ctx, params, *args.src, *args.dest, *args.resume))
}

func Do(ctx *mansion.Context, params *CopyParams, srcPath string, destPath string, resume bool) error {
	retryCtx := retrycontext.NewDefault()
	retryCtx.Settings.Consumer = comm.NewStateConsumer()
	retryCtx.Settings.MaxTries = 2

	for retryCtx.ShouldTry() {
		err := Try(ctx, params, srcPath, destPath, resume)
		if err != nil {
			if dl.IsIntegrityError(err) {
				retryCtx.Retry(err.Error())
				continue
			}

			// if it's not an integrity error, just bubble it up
			return err
		}

		return nil
	}

	return errors.New("cp: too many errors, giving up")
}

func Try(ctx *mansion.Context, params *CopyParams, srcPath string, destPath string, resume bool) error {
	consumer := params.Consumer

	src, err := eos.Open(srcPath, option.WithConsumer(consumer))
	if err != nil {
		return err
	}

	defer src.Close()

	dir := filepath.Dir(destPath)
	err = os.MkdirAll(dir, 0755)
	if err != nil {
		return err
	}

	flags := os.O_CREATE | os.O_WRONLY

	dest, err := os.OpenFile(destPath, flags, 0644)
	if err != nil {
		return err
	}

	defer dest.Close()

	stats, err := src.Stat()
	if err != nil {
		return err
	}

	totalBytes := int64(stats.Size())
	if totalBytes == 0 {
		totalBytes = -1
	}
	var startOffset int64
	var copiedBytes int64
	start := time.Now()
	var inferredTotalSize int64

	err = func() error {
		if totalBytes == -1 {
			resume = false
		}

		if resume {
			startOffset, err = dest.Seek(0, io.SeekEnd)
			if err != nil {
				return err
			}

			if startOffset == 0 {
				consumer.Infof("Downloading %s", humanize.IBytes(uint64(totalBytes)))
			} else if startOffset > totalBytes {
				consumer.Warnf("Existing data too big (%s > %s), starting over", humanize.IBytes(uint64(startOffset)), humanize.IBytes(uint64(totalBytes)))
				startOffset, err = dest.Seek(0, io.SeekStart)
				if err != nil {
					return err
				}
			} else if startOffset == totalBytes {
				consumer.Infof("All %s already there", humanize.IBytes(uint64(totalBytes)))
				return nil
			}

			consumer.Infof("Resuming at %s / %s", humanize.IBytes(uint64(startOffset)), humanize.IBytes(uint64(totalBytes)))

			_, err = src.Seek(startOffset, io.SeekStart)
			if err != nil {
				return err
			}
		} else {
			if totalBytes > 0 {
				consumer.Infof("Downloading %s", humanize.IBytes(uint64(totalBytes)))
			} else {
				consumer.Infof("Downloading (unknown size)")
			}
		}

		if totalBytes > 0 {
			initialProgress := float64(startOffset) / float64(totalBytes)
			params.OnStart(initialProgress, totalBytes)
		} else {
			params.OnStart(0, 0)
		}

		cw := counter.NewWriterCallback(func(count int64) {
			if totalBytes > 0 {
				alpha := float64(startOffset+count) / float64(totalBytes)
				consumer.Progress(alpha)
			} else {
				consumer.Progress(0)
			}
		}, dest)

		copiedBytes, err = io.Copy(cw, src)
		if err != nil {
			return err
		}
		params.OnStop()

		if totalBytes > 0 {
			err = os.Truncate(destPath, totalBytes)
			if err != nil {
				return errors.WithStack(err)
			}
			inferredTotalSize = totalBytes
		} else {
			// this only works b/c we prevent resume
			inferredTotalSize = copiedBytes
		}

		return nil
	}()

	if err != nil {
		return err
	}

	if hf, ok := src.(*httpfile.HTTPFile); ok {
		header := hf.GetHeader()
		if header != nil {
			err = dl.CheckIntegrity(comm.NewStateConsumer(), header, totalBytes, destPath)
			if err != nil {
				comm.Log("Integrity checks failed, truncating")
				os.Truncate(destPath, 0)
				return errors.WithStack(err)
			}
		} else {
			comm.Debugf("Not performing integrity checks (no header)")
		}
	} else {
		comm.Debugf("Not performing integrity checks (not an HTTP resource)")
	}

	totalDuration := time.Since(start)
	prettySize := humanize.IBytes(uint64(copiedBytes))
	perSecond := humanize.IBytes(uint64(float64(inferredTotalSize-startOffset) / totalDuration.Seconds()))

	if startOffset > 0 {
		prettyStartOffset := humanize.IBytes(uint64(startOffset))
		comm.Statf("%s + %s copied @ %s/s\n", prettyStartOffset, prettySize, perSecond)
	} else {
		comm.Statf("%s copied @ %s/s\n", prettySize, perSecond)
	}

	return nil
}
