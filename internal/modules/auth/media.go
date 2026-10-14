package auth

import "github.com/Chilly913/PTIT_ERIPT_API/internal/media"

// ObjectStore is the subset of *storage.Storage the module uses (faked in tests).
type ObjectStore = media.ObjectStore

type mediaResolver = media.Resolver

func newMediaResolver(store ObjectStore, appURL string) mediaResolver {
	return media.NewResolver(store, appURL)
}
