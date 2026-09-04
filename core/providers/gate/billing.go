package gate

import schemas "github.com/maximhq/bifrost/core/schemas"

type videoSubmissionBillingContextKey struct{}
type videoSettledBillingContextKey struct{}

var gateVideoSubmissionBillingContextKey videoSubmissionBillingContextKey
var gateVideoSettledBillingContextKey videoSettledBillingContextKey

type videoSubmissionBilling struct {
	taskID   string
	currency string
}

type videoSettledBilling struct {
	taskID     string
	billedCost string
	currency   string
}

// VideoSubmissionBillingFromContext returns the task identity and exact
// currency reported by the most recent successful VideoGeneration call.
// Missing currency stays missing; Gate's USD/USDT contract is not guessed.
func VideoSubmissionBillingFromContext(ctx *schemas.BifrostContext) (taskID, currency string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	billing, ok := ctx.Value(gateVideoSubmissionBillingContextKey).(videoSubmissionBilling)
	if !ok || billing.taskID == "" || billing.currency == "" {
		return "", "", false
	}
	return billing.taskID, billing.currency, true
}

// SettledVideoBillingFromContext returns the task identity, exact billed_cost,
// and optional currency reported by the most recent successful terminal
// VideoRetrieve call. Gate's documented retrieve response omits currency, so
// callers must persist the submission currency and fail closed if neither call
// supplies one.
func SettledVideoBillingFromContext(ctx *schemas.BifrostContext) (taskID, billedCost, currency string, ok bool) {
	if ctx == nil {
		return "", "", "", false
	}
	billing, ok := ctx.Value(gateVideoSettledBillingContextKey).(videoSettledBilling)
	if !ok || billing.taskID == "" || billing.billedCost == "" {
		return "", "", "", false
	}
	return billing.taskID, billing.billedCost, billing.currency, true
}

func clearVideoSubmissionBilling(ctx *schemas.BifrostContext) {
	if ctx != nil {
		ctx.ClearValue(gateVideoSubmissionBillingContextKey)
	}
}

func setVideoSubmissionBilling(ctx *schemas.BifrostContext, taskID, currency string) {
	if ctx != nil {
		ctx.SetValue(gateVideoSubmissionBillingContextKey, videoSubmissionBilling{
			taskID:   taskID,
			currency: currency,
		})
	}
}

func clearVideoSettledBilling(ctx *schemas.BifrostContext) {
	if ctx != nil {
		ctx.ClearValue(gateVideoSettledBillingContextKey)
	}
}

func setVideoSettledBilling(ctx *schemas.BifrostContext, taskID, billedCost, currency string) {
	if ctx != nil {
		ctx.SetValue(gateVideoSettledBillingContextKey, videoSettledBilling{
			taskID:     taskID,
			billedCost: billedCost,
			currency:   currency,
		})
	}
}

// isNonNegativeDecimal validates Gate's fixed-point money text without parsing
// it through float64, so the value left in context remains byte-exact.
func isNonNegativeDecimal(value string) bool {
	if value == "" {
		return false
	}
	dot := -1
	for i := range len(value) {
		switch value[i] {
		case '.':
			if dot >= 0 {
				return false
			}
			dot = i
		default:
			if value[i] < '0' || value[i] > '9' {
				return false
			}
		}
	}
	return dot != 0 && dot != len(value)-1
}
