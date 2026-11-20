package gormstore

import (
	"errors"
	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"

	"github.com/dujiao-next/internal/constants"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	procurementdomain "github.com/dujiao-next/internal/modules/procurement/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errDenied = errors.New("backup procurement claim denied")

type claimInput struct {
	orderID   uint
	product   backupstate.Product
	trigger   backupstate.Trigger
	productID uint
	skuID     uint
	adminID   uint
}

// ClaimPlus creates a durable Plus backup claim for an authorized admin trigger.
func ClaimPlus(db *gorm.DB, orderID, adminID uint) (*backupstate.Claim, error) {
	return claim(db, claimInput{orderID: orderID, product: backupstate.Plus, trigger: backupstate.Manual, productID: backupstate.LocalPlusProductID, skuID: backupstate.LocalPlusSKUID, adminID: adminID})
}

// ClaimPlusAutomatic creates a durable Plus backup claim after payment confirmation.
func ClaimPlusAutomatic(db *gorm.DB, orderID uint) (*backupstate.Claim, error) {
	return claim(db, claimInput{orderID: orderID, product: backupstate.Plus, trigger: backupstate.Automatic, productID: backupstate.LocalPlusProductID, skuID: backupstate.LocalPlusSKUID})
}

// ClaimGmail creates a new Gmail-only claim after payment confirmation.
func ClaimGmail(db *gorm.DB, orderID uint) (*backupstate.Claim, error) {
	return claim(db, claimInput{orderID: orderID, product: backupstate.Gmail, trigger: backupstate.Automatic, productID: backupstate.LocalGmailProductID, skuID: backupstate.LocalGmailSKUID})
}

// ClaimMuse creates a durable Muse backup claim for paid auto-buy (no primary)
// or after verified primary never-sent / no-purchase evidence.
func ClaimMuse(db *gorm.DB, orderID uint) (*backupstate.Claim, error) {
	return claim(db, claimInput{orderID: orderID, product: backupstate.Muse, trigger: backupstate.Automatic, productID: backupstate.LocalMuseProductID, skuID: backupstate.LocalMuseSKUID})
}

// claim is the durable fence used by exported claim helpers.
// Refund and manual fulfillment must first share its durable fence, otherwise a
// later action could race a supplier POST after this transaction commits.
func claim(db *gorm.DB, in claimInput) (*backupstate.Claim, error) {
	var result backupstate.Claim
	err := db.Transaction(func(tx *gorm.DB) error {
		var initial orderdomain.Order
		if err := tx.First(&initial, in.orderID).Error; err != nil {
			return err
		}
		rootID := initial.ID
		if initial.ParentID != nil {
			rootID = *initial.ParentID
		}
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&orderdomain.Order{}).Where("id = ?", rootID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}
		var root, order orderdomain.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, rootID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, in.orderID).Error; err != nil {
			return err
		}
		if root.DeletedAt != nil || order.DeletedAt != nil || root.RefundedAmount.IsPositive() || order.RefundedAmount.IsPositive() ||
			(order.Status != constants.OrderStatusPaid && order.Status != constants.OrderStatusFulfilling) ||
			(root.Status != constants.OrderStatusPaid && root.Status != constants.OrderStatusFulfilling && root.Status != constants.OrderStatusPartiallyDelivered && root.Status != constants.OrderStatusPartiallyRefunded) {
			return errDenied
		}
		var items []orderdomain.OrderItem
		if err := tx.Where("order_id = ? AND deleted_at IS NULL", order.ID).Find(&items).Error; err != nil {
			return err
		}
		if len(items) != 1 || items[0].ProductID != in.productID || items[0].SKUID != in.skuID || in.productID == 0 || in.skuID == 0 || items[0].Quantity <= 0 {
			return errDenied
		}
		var count int64
		// Include deleted delivery rows: deletion cannot prove no customer received it.
		if err := tx.Unscoped().Model(&fulfillmentdomain.Fulfillment{}).Where("order_id = ?", order.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errDenied
		}
		if err := tx.Model(&backupstate.Claim{}).Where("local_order_id = ?", order.ID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errDenied
		}
		var primary procurementdomain.Order
		err := tx.Unscoped().Where("local_order_id = ?", order.ID).First(&primary).Error
		evidence := backupstate.NoPrimary
		if err == nil {
			// Legacy, deleted, started or terminal records never prove safety.
			if primary.DeletedAt != nil || primary.SubmissionVersion != 1 || primary.SubmittedAt != nil || primary.SubmissionRequestSHA256 != "" || primary.SubmissionConnectionSHA256 != "" || primary.UpstreamOrderID != 0 || primary.UpstreamOrderNo != "" || (primary.Status != "pending" && primary.Status != "failed" && primary.Status != "rejected") {
				return errDenied
			}
			evidence = backupstate.NeverSent
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if backupstate.Evaluate(backupstate.Snapshot{Product: in.product, Trigger: in.trigger, Paid: true, PrimaryEvidence: evidence, ExistingBackup: backupstate.NoBackup, AdminAuthorized: in.adminID != 0}) != backupstate.AllowClaim {
			return errDenied
		}
		result = backupstate.Claim{LocalOrderID: order.ID, ProductID: in.productID, SKUID: in.skuID, PrimaryOrderID: primary.ID, Product: in.product, State: backupstate.Claimed, AdminID: in.adminID}
		return tx.Create(&result).Error
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
