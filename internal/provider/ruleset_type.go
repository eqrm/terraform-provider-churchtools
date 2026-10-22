package provider

import (
	"context"
	"fmt"

	"github.com/eqrm/terraform-provider-churchtools/internal/ruleset"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// RulesetType is a string holding a dynamic-group ruleset as JSON. Two values are
// semantically equal when their normalized trees are deep-equal (see
// internal/ruleset), so the state can hold the live JSON — with ChurchTools'
// cosmetic dterm labels, string ints and the `[RuleSet]` array — while the
// configuration holds the authored file, without a perpetual diff.
//
// This covers rulesets without `__ctRef` markers. A ruleset with markers only
// equals the live JSON once the markers are resolved, which needs the `refs`
// attribute; the resource's ModifyPlan does that comparison.
type RulesetType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = RulesetType{}

func (t RulesetType) Equal(o attr.Type) bool {
	other, ok := o.(RulesetType)
	return ok && t.StringType.Equal(other.StringType)
}

func (t RulesetType) String() string { return "RulesetType" }

func (t RulesetType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return RulesetValue{StringValue: in}, nil
}

func (t RulesetType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", attrValue)
	}
	v, diags := t.ValueFromString(ctx, sv)
	if diags.HasError() {
		return nil, fmt.Errorf("converting to RulesetValue: %v", diags)
	}
	return v, nil
}

func (t RulesetType) ValueType(context.Context) attr.Value { return RulesetValue{} }

// RulesetValue is the value of a RulesetType attribute.
type RulesetValue struct {
	basetypes.StringValue
}

var _ basetypes.StringValuableWithSemanticEquals = RulesetValue{}

func (v RulesetValue) Equal(o attr.Value) bool {
	other, ok := o.(RulesetValue)
	return ok && v.StringValue.Equal(other.StringValue)
}

func (v RulesetValue) Type(context.Context) attr.Type { return RulesetType{} }

// StringSemanticEquals compares the normalized trees. Unparsable JSON is never
// equal, so a broken value always shows up as a change.
func (v RulesetValue) StringSemanticEquals(_ context.Context, other basetypes.StringValuable) (bool, diag.Diagnostics) {
	o, ok := other.(RulesetValue)
	if !ok {
		return false, nil
	}
	if v.IsNull() || v.IsUnknown() || o.IsNull() || o.IsUnknown() {
		return v.Equal(o), nil
	}
	a, err := ruleset.NormalizeJSON(v.ValueString())
	if err != nil {
		return false, nil
	}
	b, err := ruleset.NormalizeJSON(o.ValueString())
	if err != nil {
		return false, nil
	}
	return ruleset.Equal(a, b), nil
}
