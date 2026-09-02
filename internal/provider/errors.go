package provider

// ErrorClass is an exact, provider-neutral classification of a response. It
// intentionally never infers state from provider prose. The Z.ai business
// codes are kept here so conformance and admission use one taxonomy without a
// package dependency cycle.
type ErrorClass string

const (
	ErrorRateLimited         ErrorClass = "rate_limited"
	ErrorOverloaded          ErrorClass = "overloaded"
	ErrorLongPeriodQuota     ErrorClass = "long_period_quota"
	ErrorPlanExpired         ErrorClass = "plan_expired"
	ErrorUnsupportedModel    ErrorClass = "unsupported_model"
	ErrorUsageRestricted     ErrorClass = "usage_policy_restricted"
	ErrorInsufficientBalance ErrorClass = "insufficient_balance"
	ErrorAuthentication      ErrorClass = "authentication"
	ErrorIdentityMismatch    ErrorClass = "identity_mismatch"
	ErrorUnknown             ErrorClass = "unknown"
)

// ClassifyProviderError performs exact classification from an HTTP status and
// normalized provider code. Business codes take precedence because a 429 may
// represent a permanent quota, plan, account, or policy condition.
func ClassifyProviderError(httpStatus int, code string) ErrorClass {
	switch code {
	case "1302", "rate_limit", "rate_limit_exceeded", "too_many_requests":
		return ErrorRateLimited
	case "1305", "overloaded", "server_overloaded", "service_unavailable":
		return ErrorOverloaded
	case "1308", "1310", "1316", "1317", "1318", "1319", "1320", "1321":
		return ErrorLongPeriodQuota
	case "1309", "1314", "plan_expired", "subscription_expired":
		return ErrorPlanExpired
	case "1311", "1211", "1212", "unsupported_model", "model_not_found", "invalid_model":
		return ErrorUnsupportedModel
	case "1301", "1313", "1315", "1220":
		return ErrorUsageRestricted
	case "1113":
		return ErrorInsufficientBalance
	case "1000", "1001", "1003", "1005", "authentication_failed", "invalid_api_key", "unauthorized":
		return ErrorAuthentication
	case "identity_mismatch":
		return ErrorIdentityMismatch
	}
	switch httpStatus {
	case 429:
		return ErrorRateLimited
	case 401, 403:
		return ErrorAuthentication
	case 503, 529:
		return ErrorOverloaded
	}
	return ErrorUnknown
}

// RequiredZAIErrorClasses are the distinct Z.ai outcomes a qualified Z.ai
// fixture must exercise. Unknown is intentionally excluded: it denotes an
// unclassified response, rather than an expected provider outcome.
func RequiredZAIErrorClasses() []ErrorClass {
	return []ErrorClass{
		ErrorRateLimited,
		ErrorOverloaded,
		ErrorLongPeriodQuota,
		ErrorPlanExpired,
		ErrorUnsupportedModel,
		ErrorUsageRestricted,
		ErrorInsufficientBalance,
		ErrorAuthentication,
	}
}
