package ante

import (
	errors "cosmossdk.io/errors"
	"cosmossdk.io/math"
	"github.com/celestiaorg/celestia-app/v2/pkg/appconsts"
	v1 "github.com/celestiaorg/celestia-app/v2/pkg/appconsts/v1"
	"github.com/celestiaorg/celestia-app/v2/x/minfee"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerror "github.com/cosmos/cosmos-sdk/types/errors"
	params "github.com/cosmos/cosmos-sdk/x/params/keeper"
)

const (
	// priorityScalingFactor is a scaling factor to convert the gas price to a priority.
	priorityScalingFactor = 1_000_000
)

// ValidateTxFee implements default fee validation logic for transactions.
// It ensures that the provided transaction fee meets a minimum threshold for the node
// as well as a global minimum threshold and computes the tx priority based on the gas price.
func ValidateTxFee(ctx sdk.Context, tx sdk.Tx, paramKeeper params.Keeper) (sdk.Coins, int64, error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return nil, 0, errors.Wrap(sdkerror.ErrTxDecode, "Tx must be a FeeTx")
	}

	fee := feeTx.GetFee().AmountOf(appconsts.BondDenom)
	gas := feeTx.GetGas()

	// Ensure that the provided fee meets a minimum threshold for the node.
	// This is only for local mempool purposes, and thus
	// is only ran on check tx.
	if ctx.IsCheckTx() {
		minGasPrice := ctx.MinGasPrices().AmountOf(appconsts.BondDenom)
		if !minGasPrice.IsZero() {
			err := verifyMinFee(fee, gas, minGasPrice, "insufficient minimum gas price for this node")
			if err != nil {
				return nil, 0, err
			}
		}
	}

	// Ensure that the provided fee meets a global minimum threshold.
	// Global minimum fee only applies to app versions greater than one
	if ctx.BlockHeader().Version.App > v1.Version {
		subspace, exists := paramKeeper.GetSubspace(minfee.ModuleName)
		if !exists {
			return nil, 0, errors.Wrap(sdkerror.ErrInvalidRequest, "minfee is not a registered subspace")
		}

		if !subspace.Has(ctx, minfee.KeyGlobalMinGasPrice) {
			return nil, 0, errors.Wrap(sdkerror.ErrKeyNotFound, "GlobalMinGasPrice")
		}

		var globalMinGasPrice sdk.Dec
		// Gets the global minimum gas price from the param store
		// panics if not configured properly
		subspace.Get(ctx, minfee.KeyGlobalMinGasPrice, &globalMinGasPrice)

		err := verifyMinFee(fee, gas, globalMinGasPrice, "insufficient gas price for the network")
		if err != nil {
			return nil, 0, err
		}
	}

	priority := getTxPriority(feeTx.GetFee(), int64(gas))
	return feeTx.GetFee(), priority, nil
}

// verifyMinFee validates that the provided transaction fee is sufficient given the provided minimum gas price.
func verifyMinFee(fee math.Int, gas uint64, minGasPrice sdk.Dec, errMsg string) error {
	// Determine the required fee by multiplying required minimum gas
	// price by the gas limit, where fee = minGasPrice * gas.
	minFee := minGasPrice.MulInt(sdk.NewIntFromUint64(gas)).Ceil()
	if fee.LT(minFee.TruncateInt()) {
		return errors.Wrapf(sdkerror.ErrInsufficientFee, "%s; got: %s required at least: %s", errMsg, fee, minFee)
	}
	return nil
}

// getTxPriority returns a naive tx priority based on the amount of the smallest denomination of the gas price
// provided in a transaction.
// NOTE: This implementation should not be used for txs with multiple coins.
func getTxPriority(fee sdk.Coins, gas int64) int64 {
	var priority int64
	for _, c := range fee {
		p := c.Amount.Mul(sdk.NewInt(priorityScalingFactor)).QuoRaw(gas)
		if !p.IsInt64() {
			continue
		}
		// take the lowest priority as the tx priority
		if priority == 0 || p.Int64() < priority {
			priority = p.Int64()
		}
	}

	return priority
}
