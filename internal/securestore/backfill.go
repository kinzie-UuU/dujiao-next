package securestore

import (
	"fmt"
	"strings"

	apicredentialdomain "github.com/dujiao-next/internal/modules/apicredential/domain"
	cardsecretdomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	securestoreserializer "github.com/dujiao-next/internal/securestore/serializer"
	"gorm.io/gorm"
)

type BackfillResult struct {
	APICredentials      int
	CardSecrets         int
	Fulfillments        int
	ProcurementPayloads int
}

// Backfill encrypts legacy plaintext values in one database transaction.
func Backfill(db *gorm.DB) (BackfillResult, error) {
	var result BackfillResult
	if db == nil {
		return result, fmt.Errorf("secure storage database is nil")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var rawSecrets []struct {
			ID     uint
			Secret string
		}
		if err := tx.Table("card_secrets").Select("id, secret").Find(&rawSecrets).Error; err != nil {
			return err
		}
		for _, raw := range rawSecrets {
			if raw.Secret == "" || securestoreserializer.IsEncryptedString(raw.Secret) {
				continue
			}
			var item cardsecretdomain.Secret
			if err := tx.First(&item, raw.ID).Error; err != nil {
				return err
			}
			if err := tx.Model(&item).Select("Secret").Updates(&item).Error; err != nil {
				return err
			}
			result.CardSecrets++
		}

		var rawFulfillments []struct {
			ID            uint
			Payload       string
			LogisticsJSON string `gorm:"column:logistics_json"`
		}
		if err := tx.Table("fulfillments").Select("id, payload, logistics_json").Find(&rawFulfillments).Error; err != nil {
			return err
		}
		for _, raw := range rawFulfillments {
			payloadPlain := raw.Payload != "" && !securestoreserializer.IsEncryptedString(raw.Payload)
			deliveryPlain := hasPlainJSON(raw.LogisticsJSON)
			if !payloadPlain && !deliveryPlain {
				continue
			}
			var item fulfillmentdomain.Fulfillment
			if err := tx.First(&item, raw.ID).Error; err != nil {
				return err
			}
			fields := make([]string, 0, 2)
			if payloadPlain {
				fields = append(fields, "Payload")
			}
			if deliveryPlain {
				fields = append(fields, "LogisticsJSON")
			}
			if err := tx.Model(&item).Select(fields).Updates(&item).Error; err != nil {
				return err
			}
			result.Fulfillments++
		}
		var rawProcurements []struct {
			ID              uint
			UpstreamPayload string
		}
		if err := tx.Table("procurement_orders").Select("id", "upstream_payload").Where("upstream_payload <> ''").Find(&rawProcurements).Error; err != nil {
			return err
		}
		for _, raw := range rawProcurements {
			if securestoreserializer.IsEncryptedString(raw.UpstreamPayload) {
				continue
			}
			item := procurementdomain.Order{ID: raw.ID, UpstreamPayload: raw.UpstreamPayload}
			if err := tx.Model(&item).Select("UpstreamPayload").UpdateColumns(&item).Error; err != nil {
				return err
			}
			result.ProcurementPayloads++
		}
		var rawCredentials []struct {
			ID        uint
			ApiSecret string
		}
		if err := tx.Table("api_credentials").Select("id, api_secret").Where("api_secret <> ''").Find(&rawCredentials).Error; err != nil {
			return err
		}
		for _, raw := range rawCredentials {
			if securestoreserializer.IsEncryptedString(raw.ApiSecret) {
				continue
			}
			item := apicredentialdomain.ApiCredential{ID: raw.ID, ApiSecret: raw.ApiSecret}
			if err := tx.Model(&item).Select("ApiSecret").UpdateColumns(&item).Error; err != nil {
				return err
			}
			result.APICredentials++
		}
		return nil
	})
	return result, err
}

func hasPlainJSON(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return false
	}
	return !securestoreserializer.IsEncryptedJSON(raw)
}
