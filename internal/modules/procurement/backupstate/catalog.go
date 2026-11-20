package backupstate

// Production catalog pins for Fang Fang backup procurement.
// Wrong IDs fail closed; do not auto-discover or remap at runtime.
const (
	LocalMuseProductID  = 50
	LocalMuseSKUID      = 85
	LocalGmailProductID = 51
	LocalGmailSKUID     = 86
	// Historical Plus identities are retained only for receipt interpretation.
	LocalPlusProductID = 41
	LocalPlusSKUID     = 74

	UpstreamGmailProductID = "6ae37dc5ed8e0e40e4de28b7"
	UpstreamMuseProductID  = "6aeedc6cadf360530f5faf80"
	UpstreamPlusProductID  = "6a4b4d7d1827db4ca39bb8be"

	GmailPreflightMaxUSD = "1.49"
	MusePreflightMaxUSD  = "1.20"
	PlusPreflightMaxUSD  = "7.00"

	LocalMuseSlug = "muse-31b-first-login"
	LocalPlusSlug = "gpt-plus-account-preorder"
)
