// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package l2pricing

import (
	"fmt"
	"testing"

	"github.com/offchainlabs/nitro/arbos/burn"
	"github.com/offchainlabs/nitro/arbos/storage"
	"github.com/offchainlabs/nitro/util/arbmath"
	"github.com/offchainlabs/nitro/util/colors"
	"github.com/offchainlabs/nitro/util/testhelpers"
)

func PricingForTest(t *testing.T, arbosVersion uint64) *L2PricingState {
	storage := storage.NewMemoryBacked(burn.NewSystemBurner(nil, false))
	err := InitializeL2PricingState(storage, arbosVersion)
	Require(t, err)
	return OpenL2PricingState(storage)
}

func fakeBlockUpdate(t *testing.T, pricing *L2PricingState, gasUsed int64, timePassed uint64, arbosVersion uint64) {
	basefee := getPrice(t, pricing)
	pricing.storage.Burner().Restrict(pricing.AddToGasPool(-gasUsed, arbosVersion))
	pricing.UpdatePricingModel(arbmath.UintToBig(basefee), timePassed, arbosVersion, true)
}

func TestPricingModelPreExp(t *testing.T) {
	versionedTestPricingModel(t, FirstExponentialPricingVersion-1)
}

func TestPricingModelExp(t *testing.T) {
	versionedTestPricingModel(t, FirstExponentialPricingVersion)
}

func versionedTestPricingModel(t *testing.T, arbosVersion uint64) {
	pricing := PricingForTest(t, arbosVersion)
	maxPool := maxGasPool(t, pricing)
	gasPool := getGasPool(t, pricing)
	minPrice := getMinPrice(t, pricing)
	price := getPrice(t, pricing)
	limit := getSpeedLimit(t, pricing)

	if gasPool != maxPool {
		Fail(t, "pool not filled", gasPool, maxPool)
	}
	if price != minPrice {
		Fail(t, "price not minimal", price, minPrice)
	}

	// declare that we've been running at the speed limit
	pricing.SetRateEstimate(limit)

	// show that running at the speed limit with a full pool is a steady-state
	colors.PrintBlue("full pool & speed limit")
	for seconds := 0; seconds < 4; seconds++ {
		fakeBlockUpdate(t, pricing, int64(seconds)*int64(limit), uint64(seconds), arbosVersion)
		if getPrice(t, pricing) != minPrice {
			Fail(t, "price changed when it shouldn't have")
		}
		if arbosVersion < 4 && getGasPool(t, pricing) != maxPool {
			Fail(t, "pool changed when it shouldn't have")
		}
	}

	// set the gas pool to the target
	target, _ := pricing.GasPoolTarget()
	poolTarget := int64(target) * maxPool / 10000
	Require(t, pricing.SetGasPool_preExp(poolTarget))
	pricing.SetGasPoolLastBlock(poolTarget)
	pricing.SetRateEstimate(limit)

	// show that running at the speed limit with a target pool is close to a steady-state
	// note that for large enough spans of time the price will rise a miniscule amount due to the pool's avg
	colors.PrintBlue("pool target & speed limit")
	for seconds := 0; seconds < 4; seconds++ {
		fakeBlockUpdate(t, pricing, int64(seconds)*int64(limit), uint64(seconds), arbosVersion)
		if getPrice(t, pricing) != minPrice {
			Fail(t, "price changed when it shouldn't have")
		}
		if arbosVersion < FirstExponentialPricingVersion && getGasPool(t, pricing) != poolTarget {
			Fail(t, "pool changed when it shouldn't have")
		}
	}

	// fill the gas pool
	Require(t, pricing.SetGasPool_preExp(maxPool))
	pricing.SetGasPoolLastBlock(maxPool)

	// show that running over the speed limit escalates the price before the pool drains
	colors.PrintBlue("exceeding the speed limit")
	for {
		fakeBlockUpdate(t, pricing, 8*int64(limit), 1, arbosVersion)
		if arbosVersion < FirstExponentialPricingVersion && getGasPool(t, pricing) < poolTarget {
			Fail(t, "the price failed to rise before the pool drained")
		}
		newPrice := getPrice(t, pricing)
		if newPrice < price {
			Fail(t, "the price shouldn't have fallen")
		}
		if newPrice > price {
			break
		}
		price = newPrice
	}

	// empty the pool
	pricing.SetRateEstimate(limit)
	price = getPrice(t, pricing)
	rate := rateEstimate(t, pricing)
	if arbosVersion < FirstExponentialPricingVersion {
		Require(t, pricing.SetGasPool_preExp(0))
		pricing.SetGasPoolLastBlock(0)
	} else {
		Require(t, pricing.SetGasBacklog(100000000))
	}

	// show that nothing happens when no time has passed and no gas has been burnt
	colors.PrintBlue("nothing should happen")
	fakeBlockUpdate(t, pricing, 0, 0, arbosVersion)
	if arbosVersion < FirstExponentialPricingVersion && (getPrice(t, pricing) != price || getGasPool(t, pricing) != 0 || rateEstimate(t, pricing) != rate) {
		Fail(t, "state shouldn't have changed")
	}

	// show that the pool will escalate the price
	colors.PrintBlue("gas pool is empty")
	fakeBlockUpdate(t, pricing, 0, 1, arbosVersion)
	if getPrice(t, pricing) <= price {
		fmt.Println(price, getPrice(t, pricing))
		Fail(t, "price should have risen")
	}
}

func maxGasPool(t *testing.T, pricing *L2PricingState) int64 {
	value, err := pricing.GasPoolMax()
	Require(t, err)
	return value
}

func getGasPool(t *testing.T, pricing *L2PricingState) int64 {
	value, err := pricing.GasPool_preExp()
	Require(t, err)
	return value
}

func getPrice(t *testing.T, pricing *L2PricingState) uint64 {
	value, err := pricing.BaseFeeWei()
	Require(t, err)
	return arbmath.BigToUintOrPanic(value)
}

func getMinPrice(t *testing.T, pricing *L2PricingState) uint64 {
	value, err := pricing.MinBaseFeeWei()
	Require(t, err)
	return arbmath.BigToUintOrPanic(value)
}

func getSpeedLimit(t *testing.T, pricing *L2PricingState) uint64 {
	value, err := pricing.SpeedLimitPerSecond()
	Require(t, err)
	return value
}

func rateEstimate(t *testing.T, pricing *L2PricingState) uint64 {
	value, err := pricing.RateEstimate()
	Require(t, err)
	return value
}

func Require(t *testing.T, err error, printables ...interface{}) {
	t.Helper()
	testhelpers.RequireImpl(t, err, printables...)
}

func Fail(t *testing.T, printables ...interface{}) {
	t.Helper()
	testhelpers.FailImpl(t, printables...)
}
