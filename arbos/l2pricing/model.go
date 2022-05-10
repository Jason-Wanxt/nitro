// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package l2pricing

import (
	"math/big"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/util/arbmath"
	"github.com/offchainlabs/nitro/util/colors"
)

const InitialSpeedLimitPerSecond = 1000000
const InitialPerBlockGasLimit uint64 = 20 * 1000000
const InitialMinimumBaseFeeWei = params.GWei / 10
const InitialBaseFeeWei = InitialMinimumBaseFeeWei
const InitialGasPoolSeconds = 10 * 60
const InitialRateEstimateInertia = 60
const InitialPricingInertia = 102
const InitialBacklogTolerance = 10

var InitialGasPoolTargetBips = arbmath.PercentToBips(80)
var InitialGasPoolWeightBips = arbmath.PercentToBips(60)

const FirstExponentialPricingVersion = 4

func (ps *L2PricingState) AddToGasPool(gas int64, arbosVersion uint64) error {
	if arbosVersion < FirstExponentialPricingVersion {
		return ps.AddToGasPool_preExp(gas)
	}

	backlog, err := ps.GasBacklog()
	if err != nil {
		return err
	}
	// pay off some of the backlog with the added gas, stopping at 0
	backlog = arbmath.SaturatingUCast(arbmath.SaturatingSub(int64(backlog), gas))
	return ps.SetGasBacklog(backlog)
}

func (ps *L2PricingState) AddToGasPool_preExp(gas int64) error {
	gasPool, err := ps.GasPool_preExp()
	if err != nil {
		return err
	}
	return ps.SetGasPool_preExp(arbmath.SaturatingAdd(gasPool, gas))
}

// Update the pricing model with info from the last block
func (ps *L2PricingState) UpdatePricingModel(l2BaseFee *big.Int, timePassed uint64, arbosVersion uint64, debug bool) {
	if arbosVersion < FirstExponentialPricingVersion {
		// note: if we restart the chain at version >= FirstExponentialPricingVersion,
		// we can simplify the L2-pricing-related params and precompiles
		ps.UpdatePricingModel_preExp(l2BaseFee, timePassed, arbosVersion, debug)
		return
	}

	speedLimit, _ := ps.SpeedLimitPerSecond()
	_ = ps.AddToGasPool(int64(timePassed*speedLimit), arbosVersion)
	inertia, _ := ps.PricingInertia()
	tolerance, _ := ps.BacklogTolerance()
	backlog, _ := ps.GasBacklog()
	minBaseFee, _ := ps.MinBaseFeeWei()
	baseFee := minBaseFee
	if backlog > tolerance*speedLimit {
		excess := int64(backlog - tolerance*speedLimit)
		exponentBips := arbmath.NaturalToBips(excess) / arbmath.Bips(inertia*speedLimit)
		baseFee = arbmath.BigMulByBips(minBaseFee, arbmath.ApproxExpBasisPoints(exponentBips))
	}
	_ = ps.SetBaseFeeWei(baseFee)
}

func (ps *L2PricingState) UpdatePricingModel_preExp(l2BaseFee *big.Int, timePassed uint64, arbosVersion uint64, debug bool) {

	// update the rate estimate, which is the weighted average of the past and present
	//     rate' = weighted average of the historical rate and the current
	//     rate' = (memory * rate + passed * recent) / (memory + passed)
	//     rate' = (memory * rate + used) / (memory + passed)
	//
	gasPool, _ := ps.GasPool_preExp()
	gasPoolLastBlock, _ := ps.GasPoolLastBlock()
	poolMax, _ := ps.GasPoolMax()
	gasPool = arbmath.MinInt(gasPool, poolMax)
	gasPoolLastBlock = arbmath.MinInt(gasPoolLastBlock, poolMax)
	gasUsed := uint64(gasPoolLastBlock - gasPool)
	rateSeconds, _ := ps.RateEstimateInertia()
	priorRate, _ := ps.RateEstimate()
	rate := arbmath.SaturatingUAdd(arbmath.SaturatingUMul(rateSeconds, priorRate), gasUsed) / (rateSeconds + timePassed)
	ps.SetRateEstimate(rate)

	// compute the rate ratio
	//     ratio = recent gas consumption rate / speed limit
	//
	speedLimit, _ := ps.SpeedLimitPerSecond()
	rateRatio := arbmath.UfracToBigFloat(rate, speedLimit)

	// compute the pool fullness ratio & the updated gas pool
	//     ratio = max(0, 2 - (average fullness) / (target fullness))
	//     pool' = min(maximum, pool + speed * passed)
	//
	timeToFull := (poolMax - gasPool) / int64(speedLimit)
	var averagePool uint64
	var newGasPool int64
	if timePassed > uint64(timeToFull) {
		spaceBefore := uint64(poolMax - gasPool)
		averagePool = uint64(poolMax) - spaceBefore*spaceBefore/arbmath.SaturatingUMul(2*speedLimit, timePassed)
		newGasPool = poolMax
	} else {
		averagePool = uint64(gasPool) + timePassed*speedLimit/2
		newGasPool = gasPool + int64(speedLimit*timePassed)
	}
	poolTarget, _ := ps.GasPoolTarget()
	poolTargetGas := uint64(arbmath.IntMulByBips(poolMax, poolTarget))
	poolRatio := arbmath.UfracToBigFloat(0, 1)
	if averagePool < 2*poolTargetGas {
		poolRatio = arbmath.UfracToBigFloat(2*poolTargetGas-averagePool, poolTargetGas)
	}

	// take the weighted average of the ratios, in basis points
	//      average = weight * pool + (1 - weight) * rate
	//
	poolWeight, _ := ps.GasPoolWeight()
	oneInBips := arbmath.OneInBips
	averageOfRatiosRaw, _ := arbmath.BigAddFloat(
		arbmath.BigFloatMulByUint(poolRatio, uint64(poolWeight)),
		arbmath.BigFloatMulByUint(rateRatio, uint64(oneInBips-poolWeight)),
	).Uint64()
	averageOfRatios := arbmath.Bips(averageOfRatiosRaw)
	averageOfRatiosUnbounded := averageOfRatios
	if arbosVersion < 3 && averageOfRatios > arbmath.PercentToBips(200) {
		averageOfRatios = arbmath.PercentToBips(200)
	}

	// update the gas price, adjusting each second by the max allowed by EIP 1559
	//      price' = price * exp(seconds at intensity) / 2 mins
	//
	exp := (averageOfRatios - arbmath.OneInBips) * arbmath.Bips(timePassed) / 120 // limit to EIP 1559's max rate
	price := arbmath.BigMulByBips(l2BaseFee, arbmath.ApproxExpBasisPoints(exp))
	maxPrice := arbmath.BigMulByInt(l2BaseFee, params.ElasticityMultiplier)
	minPrice, _ := ps.MinBaseFeeWei()

	p := func(args ...interface{}) {
		if debug {
			colors.PrintGrey(args...)
		}
	}
	p("\nused\t", gasUsed, " in ", timePassed, "s = ", rate, "/s vs limit ", speedLimit, "/s for ", rateRatio)
	p("pool\t", gasPool, "/", poolMax, " ➤ ", averagePool, " ➤ ", newGasPool, " ", poolRatio)
	p("ratio\t", poolRatio, rateRatio, " ➤ ", averageOfRatiosUnbounded, "‱  ")
	p("exp()\t", exp, " ➤ ", arbmath.ApproxExpBasisPoints(exp), "‱  ")
	p("price\t", l2BaseFee, " ➤ ", price, " bound to [", minPrice, ", ", maxPrice, "]\n")

	if arbmath.BigLessThan(price, minPrice) {
		price = minPrice
	}
	if arbmath.BigGreaterThan(price, maxPrice) {
		log.Warn("ArbOS tried to 2x the price", "price", price, "bound", maxPrice)
		price = maxPrice
	}
	_ = ps.SetBaseFeeWei(price)
	_ = ps.SetGasPool_preExp(newGasPool)
	ps.SetGasPoolLastBlock(newGasPool)
}

func (ps *L2PricingState) PerBlockGasLimit(arbosVersion uint64) (uint64, error) {
	if arbosVersion >= FirstExponentialPricingVersion {
		return ps.MaxPerBlockGasLimit()
	}
	pool, _ := ps.GasPool_preExp()
	maxLimit, err := ps.MaxPerBlockGasLimit()
	if pool < 0 || err != nil {
		return 0, err
	} else if uint64(pool) > maxLimit {
		return maxLimit, nil
	} else {
		return uint64(pool), nil
	}
}
