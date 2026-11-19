package backupstate

// Production catalog pins for Fang Fang backup procurement.
// Wrong IDs fail closed; do not auto-discover or remap at runtime.
const (
	LocalMuseProductID = 37
	LocalMuseSKUID     = 70
	LocalPlusProductID = 41
	LocalPlusSKUID     = 74

	UpstreamMuseProductID = "6aeedc6cadf360530f5faf80"
	UpstreamPlusProductID = "6a4b4d7d1827db4ca39bb8be"

	MusePreflightMaxUSD = "1.20"
	PlusPreflightMaxUSD = "7.00"
)
