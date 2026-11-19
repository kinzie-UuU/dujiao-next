package backupstate

// Production catalog pins for Fang Fang backup procurement.
// Wrong IDs fail closed; do not auto-discover or remap at runtime.
const (
	LocalMuseProductID = 50
	LocalMuseSKUID     = 85
	LocalPlusProductID = 51
	LocalPlusSKUID     = 86

	UpstreamMuseProductID = "6aeedc6cadf360530f5faf80"
	UpstreamPlusProductID = "6a4b4d7d1827db4ca39bb8be"

	MusePreflightMaxUSD = "1.20"
	PlusPreflightMaxUSD = "7.00"

	LocalMuseSlug = "muse-31b-first-login"
	LocalPlusSlug = "gmail-aged-2010-2022"
)
