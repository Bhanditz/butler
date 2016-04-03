package main

import (
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"

	"github.com/itchio/butler/comm"

	"gopkg.in/alecthomas/kingpin.v2"
)

// #cgo windows LDFLAGS: -Wl,--allow-multiple-definition -static
import "C"

var (
	version = "head" // set by command-line on CI release builds
	app     = kingpin.New("butler", "Your happy little itch.io helper")

	dlCmd = app.Command("dl", "Download a file (resumes if can, checks hashes)").Hidden()

	untarCmd = app.Command("untar", "Extract a .tar file").Hidden()
	wipeCmd  = app.Command("wipe", "Completely remove a directory (rm -rf)").Hidden()
	dittoCmd = app.Command("ditto", "Create a mirror (incl. symlinks) of a directory into another dir (rsync -az)").Hidden()
	mkdirCmd = app.Command("mkdir", "Create an empty directory and all required parent directories (mkdir -p)").Hidden()

	walkCmd = app.Command("walk", "Print TLC tree for given directory as JSON").Hidden()

	loginCmd  = app.Command("login", "Connect butler to your itch.io account and save credentials locally.")
	logoutCmd = app.Command("logout", "Remove saved itch.io credentials.")
	pushCmd   = app.Command("push", "Upload a new build to itch.io. See `butler help push`.")
	fetchCmd  = app.Command("fetch", "Download and extract the latest build of a channel from itch.io")
	statusCmd = app.Command("status", "Show a list of channels and the status of their latest and pending builds.")

	signCmd   = app.Command("sign", "(Advanced) Generate a signature file for a given directory. Useful for integrity checks and remote diff generation.").Hidden()
	verifyCmd = app.Command("verify", "(Advanced) Use a signature to verify the integrity of a directory").Hidden()
	diffCmd   = app.Command("diff", "(Advanced) Compute the difference between two directories (fast) or .zip archives (slow). Stores the patch in `patch.pwr`, and a signature in `patch.pwr.sig` for integrity checks and further diff.").Hidden()
	applyCmd  = app.Command("apply", "(Advanced) Use a patch to patch a directory to a new version").Hidden()
)

var appArgs = struct {
	json       *bool
	quiet      *bool
	verbose    *bool
	timestamps *bool
	noProgress *bool
	panic      *bool

	identity *string
	address  *string
}{
	app.Flag("json", "Enable machine-readable JSON-lines output").Hidden().Short('j').Bool(),
	app.Flag("quiet", "Hide progress indicators & other extra info").Hidden().Short('q').Bool(),
	app.Flag("verbose", "Be very chatty about what's happening").Short('v').Bool(),
	app.Flag("timestamps", "Prefix all output by timestamps (for logging purposes)").Hidden().Bool(),
	app.Flag("noprogress", "Doesn't show progress bars").Hidden().Bool(),
	app.Flag("panic", "Panic on error").Hidden().Bool(),

	app.Flag("identity", "Path to your itch.io API token").Default(defaultKeyPath()).Short('i').String(),
	app.Flag("address", "itch.io server to talk to").Default("https://itch.io").Short('a').Hidden().String(),
}

var dlArgs = struct {
	url  *string
	dest *string

	thorough *bool
}{
	dlCmd.Arg("url", "Address to download from").Required().String(),
	dlCmd.Arg("dest", "File to write downloaded data to").Required().String(),

	dlCmd.Flag("thorough", "Check all available hashes").Bool(),
}

func defaultKeyPath() string {
	dir := ".itch"
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}

	if runtime.GOOS == "darwin" {
		home = path.Join(home, "Library", "Application Support")
		dir = "itch"
	}

	return filepath.FromSlash(path.Join(home, dir, "butler_creds"))
}

var pushArgs = struct {
	src    *string
	target *string
}{
	pushCmd.Arg("src", "Directory to upload. May also be a zip archive (slower)").Required().String(),
	pushCmd.Arg("target", "Where to push, for example 'leafo/xmoon:win-64'. Targets are of the form project:channel, where project is username/game or game_id.").Required().String(),
}

var fetchArgs = struct {
	target *string
	out    *string
}{
	fetchCmd.Arg("target", "Which user/project:channel to fetch from, for example 'leafo/xmoon:win-64'. Targets are of the form project:channel where project is username/game or game_id.").Required().String(),
	fetchCmd.Arg("out", "Directory to fetch and extract build to").Required().String(),
}

var statusArgs = struct {
	target *string
}{
	statusCmd.Arg("target", "Which user/project:channel to fetch from, for example 'leafo/xmoon:win-64'. Targets are of the form project:channel where project is username/game or game_id.").Required().String(),
}

var untarArgs = struct {
	file *string
	dir  *string
}{
	untarCmd.Arg("file", "Path of the .tar archive to extract").Required().String(),
	untarCmd.Flag("dir", "An optional directory to which to extract files (defaults to CWD)").Default(".").Short('d').String(),
}

var wipeArgs = struct {
	path *string
}{
	wipeCmd.Arg("path", "Path to completely remove, including its contents").Required().String(),
}

var mkdirArgs = struct {
	path *string
}{
	mkdirCmd.Arg("path", "Directory to create").Required().String(),
}

var dittoArgs = struct {
	src *string
	dst *string
}{
	dittoCmd.Arg("src", "Directory to mirror").Required().ExistingFileOrDir(),
	dittoCmd.Arg("dst", "Path where to create a mirror").Required().String(),
}

var walkArgs = struct {
	src *string
}{
	walkCmd.Arg("src", "Directory to walk").Required().ExistingFileOrDir(),
}

var diffArgs = struct {
	old   *string
	new   *string
	patch *string

	verify *bool

	quality *int
}{
	diffCmd.Arg("old", "Directory or .zip archive (slower) with older files, or signature file generated from old directory.").Required().String(),
	diffCmd.Arg("new", "Directory or .zip archive (slower) with newer files").Required().String(),
	diffCmd.Arg("patch", "Path to write the patch file (recommended extension is `.pwr`) The signature file will be written to the same path, with .sig added to the end.").Default("patch.pwr").String(),

	diffCmd.Flag("verify", "Make sure generated patch applies cleanly by applying it (slower)").Bool(),

	diffCmd.Flag("quality", "Compression quality").Hidden().Default("1").Int(),
}

var applyArgs = struct {
	patch *string
	old   *string

	dir     *string
	verify  *string
	reverse *string
	inplace *bool
}{
	applyCmd.Arg("patch", "Patch file (.pwr), previously generated with the `diff` command.").Required().ExistingFileOrDir(),
	applyCmd.Arg("old", "Directory to patch").Required().ExistingFileOrDir(),

	applyCmd.Flag("dir", "Directory to create newer files in, instead of working in-place").Short('d').String(),
	applyCmd.Flag("verify", "When given, verifies patch application on-the-fly, and abort if any integrity check fails").String(),
	applyCmd.Flag("reverse", "When given, generates a reverse patch to allow rolling back later, along with its signature").String(),
	applyCmd.Flag("inplace", "Apply patch directly to old directory. Required for safety").Bool(),
}

var verifyArgs = struct {
	signature *string
	output    *string
}{
	verifyCmd.Arg("signature", "Path to read signature file from").Required().String(),
	verifyCmd.Arg("dir", "Path of directory to verify").Required().String(),
}

var signArgs = struct {
	output    *string
	signature *string
}{
	signCmd.Arg("dir", "Path of directory to sign").Required().String(),
	signCmd.Arg("signature", "Path to write signature to").Required().String(),
}

func must(err error) {
	if err != nil {
		comm.Die(err.Error())
	}
}

func main() {
	app.HelpFlag.Short('h')
	app.Version(version)
	app.VersionFlag.Short('V')

	cmd, err := app.Parse(os.Args[1:])
	if *appArgs.timestamps {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	} else {
		log.SetFlags(0)
	}

	if *appArgs.quiet {
		*appArgs.noProgress = true
		*appArgs.verbose = false
	}

	if !isTerminal() {
		log.Println("Not a terminal, disabling progress indicator")
		*appArgs.noProgress = true
	}
	comm.Configure(*appArgs.noProgress, *appArgs.quiet, *appArgs.verbose, *appArgs.json, *appArgs.panic)

	switch kingpin.MustParse(cmd, err) {
	case dlCmd.FullCommand():
		dl(*dlArgs.url, *dlArgs.dest)

	case loginCmd.FullCommand():
		login()

	case logoutCmd.FullCommand():
		logout()

	case pushCmd.FullCommand():
		push(*pushArgs.src, *pushArgs.target)

	case fetchCmd.FullCommand():
		fetch(*fetchArgs.target, *fetchArgs.out)

	case statusCmd.FullCommand():
		status(*statusArgs.target)

	case untarCmd.FullCommand():
		untar(*untarArgs.file, *untarArgs.dir)

	case wipeCmd.FullCommand():
		wipe(*wipeArgs.path)

	case mkdirCmd.FullCommand():
		mkdir(*mkdirArgs.path)

	case dittoCmd.FullCommand():
		ditto(*dittoArgs.src, *dittoArgs.dst)

	case walkCmd.FullCommand():
		walk(*walkArgs.src)

	case diffCmd.FullCommand():
		diff(*diffArgs.old, *diffArgs.new, *diffArgs.patch, *diffArgs.quality)

	case applyCmd.FullCommand():
		apply(*applyArgs.patch, *applyArgs.old, *applyArgs.dir, *applyArgs.inplace)

	case verifyCmd.FullCommand():
		verify(*verifyArgs.signature, *verifyArgs.output)

	case signCmd.FullCommand():
		sign(*signArgs.output, *signArgs.signature)
	}
}
