package index

import (
	"fmt"
)

type WeightProfile struct {
	Name           string
	KeywordWeight  float32
	VectorWeight   float32
	RecencyWeight  float32
	PathMatchBonus float32
}

var (
	ProfileCode = WeightProfile{
		Name:           "code",
		KeywordWeight:  1.3,
		VectorWeight:   0.8,
		RecencyWeight:  0.3,
		PathMatchBonus: 1.0,
	}

	ProfileProse = WeightProfile{
		Name:           "prose",
		KeywordWeight:  0.7,
		VectorWeight:   1.4,
		RecencyWeight:  0.2,
		PathMatchBonus: 0.5,
	}

	ProfileMixed = WeightProfile{
		Name:           "mixed",
		KeywordWeight:  1.0,
		VectorWeight:   1.0,
		RecencyWeight:  0.3,
		PathMatchBonus: 0.8,
	}

	ProfileBalanced = WeightProfile{
		Name:           "balanced",
		KeywordWeight:  1.0,
		VectorWeight:   1.0,
		RecencyWeight:  0.15,
		PathMatchBonus: 1.0,
	}
)

var weightProfiles = map[string]WeightProfile{
	"code":     ProfileCode,
	"prose":    ProfileProse,
	"mixed":    ProfileMixed,
	"balanced": ProfileBalanced,
}

func GetWeightProfile(name string) (WeightProfile, error) {
	if name == "" {
		return ProfileBalanced, nil
	}
	p, ok := weightProfiles[name]
	if !ok {
		return WeightProfile{}, fmt.Errorf("unknown weight profile %q; available: code, prose, mixed, balanced", name)
	}
	return p, nil
}

func ListWeightProfiles() []string {
	names := make([]string, 0, len(weightProfiles))
	for k := range weightProfiles {
		names = append(names, k)
	}
	return names
}

func (p WeightProfile) ApplyWeights(weights *QuerySignalWeights) {
	if p.KeywordWeight > 0 {
		weights.Keyword = p.KeywordWeight
	}
	if p.VectorWeight > 0 {
		weights.Vector = p.VectorWeight
	}
}
