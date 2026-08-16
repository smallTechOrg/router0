package routing

// Tier represents the quality tier requested by the client.
type Tier int

const (
	TierFlashLite Tier = iota // all models
	TierFlash                 // ≥70B params, ≥100K context
	TierPro                   // ≥150B params, ≥200K context
)

func (t Tier) String() string {
	switch t {
	case TierPro:
		return "pro"
	case TierFlash:
		return "flash"
	default:
		return "flash-lite"
	}
}

// TierFromString parses a pool name string back to a Tier.
func TierFromString(s string) Tier {
	switch s {
	case "pro":
		return TierPro
	case "flash":
		return TierFlash
	default:
		return TierFlashLite
	}
}

// TierFromModel maps the client-facing model name to a routing tier.
func TierFromModel(model string) Tier {
	switch model {
	case "zer0-0.1-pro":
		return TierPro
	case "zer0-0.1-flash":
		return TierFlash
	case "zer0-0.1-flash-lite":
		return TierFlashLite
	default:
		return TierFlash
	}
}

type TierFilter struct {
	MinParamB  float64
	MinContext int
}

func (t Tier) Filter() TierFilter {
	switch t {
	case TierPro:
		return TierFilter{MinParamB: 150, MinContext: 200000}
	case TierFlash:
		return TierFilter{MinParamB: 70, MinContext: 100000}
	default:
		return TierFilter{MinParamB: 0, MinContext: 0}
	}
}
