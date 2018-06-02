package fetch

import (
	"github.com/itchio/butler/butlerd"
	"github.com/itchio/butler/butlerd/messages"
	"github.com/itchio/butler/database/models"
	itchio "github.com/itchio/go-itchio"
	"github.com/itchio/hades"
	"github.com/pkg/errors"
)

func FetchCollection(rc *butlerd.RequestContext, params *butlerd.FetchCollectionParams) (*butlerd.FetchCollectionResult, error) {
	consumer := rc.Consumer
	conn := rc.DBPool.Get(rc.Ctx.Done())
	defer rc.DBPool.Put(conn)

	if params.CollectionID == 0 {
		return nil, errors.New("collectionId must be non-zero")
	}

	_, client := rc.ProfileClient(params.ProfileID)

	sendDBCollection := func() error {
		collection := models.CollectionByID(conn, params.CollectionID)
		if collection == nil {
			return nil
		}

		models.CollectionExt(collection).PreloadCollectionGames(conn)

		err := messages.FetchCollectionYield.Notify(rc, &butlerd.FetchCollectionYieldNotification{Collection: collection})
		if err != nil {
			return errors.WithStack(err)
		}

		return nil
	}

	err := sendDBCollection()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	consumer.Debugf("Querying API...")
	collRes, err := client.GetCollection(&itchio.GetCollectionParams{
		CollectionID: params.CollectionID,
	})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	collection := collRes.Collection
	collection.CollectionGames = nil

	models.MustSaveOne(conn, collRes.Collection)

	// after collection metadata update
	err = sendDBCollection()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var offset int64
	for page := int64(1); ; page++ {
		consumer.Infof("Fetching page %d", page)

		gamesRes, err := client.GetCollectionGames(&itchio.GetCollectionGamesParams{
			CollectionID: params.CollectionID,
			Page:         page,
		})
		if err != nil {
			return nil, errors.WithStack(err)
		}
		numPageGames := int64(len(gamesRes.CollectionGames))

		if numPageGames == 0 {
			break
		}

		for _, cg := range gamesRes.CollectionGames {
			collection.CollectionGames = append(collection.CollectionGames, cg)
		}

		models.MustSave(conn, &hades.SaveParams{
			Record: collection,
			Assocs: []string{"CollectionGames"},
			DontCull: []interface{}{
				&itchio.CollectionGame{},
			},
		})

		offset += numPageGames

		if offset >= collection.GamesCount {
			// already fetched all or more?!
			break
		}

		if numPageGames < gamesRes.PerPage {
			// that probably means there's no more pages
			break
		}

		// after each page of games fetched
		err = sendDBCollection()
		if err != nil {
			return nil, errors.WithStack(err)
		}
	}

	models.MustSave(conn, &hades.SaveParams{
		Record: collection,
		Assocs: []string{"CollectionGames"},
	})

	// after all pages are fetched
	err = sendDBCollection()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	res := &butlerd.FetchCollectionResult{}
	return res, nil
}
