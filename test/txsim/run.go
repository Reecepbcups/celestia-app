package txsim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/celestiaorg/celestia-app/app/encoding"
	"github.com/celestiaorg/celestia-app/pkg/user"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const DefaultSeed = 900183116

// Run is the entrypoint function for starting the txsim client. The lifecycle of the client is managed
// through the context. At least one grpc and rpc endpoint must be provided. The client relies on a
// single funded master account present in the keyring. The client allocates subaccounts for sequences
// at runtime. A seed can be provided for deterministic randomness. The pollTime dictates the frequency
// that the client should check for updates from state and that transactions have been committed.
//
// This should be used for testing purposes only.
//
// All sequences can be scaled up using the `Clone` method. This allows for a single sequence that
// repeatedly sends random PFBs to be scaled up to 1000 accounts sending PFBs.
func Run(
	ctx context.Context,
	grpcEndpoint string,
	keys keyring.Keyring,
	encCfg encoding.Config,
	opts *Options,
	sequences ...Sequence,
) error {
	opts.Fill()
	r := rand.New(rand.NewSource(opts.seed))

	conn, err := grpc.Dial(grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dialing %s: %w", grpcEndpoint, err)
	}

	logger := zerolog.Nop()
	if !opts.suppressLogger {
		hash := sha256.Sum256([]byte(opts.masterAcc + grpcEndpoint))
		id := hex.EncodeToString(hash[:10])
		logger = zerolog.New(os.Stdout).With().Str("id", id).Logger()
		logger.Info().Int64("seed", opts.seed).Str("master", opts.masterAcc).Str("grpc", grpcEndpoint).Msg("starting txsim")
	}

	// Create the account manager to handle account transactions.
	manager, err := NewAccountManager(ctx, keys, encCfg, opts.masterAcc, conn, opts.pollTime, opts.useFeeGrant, logger)
	if err != nil {
		return err
	}

	// Initialize each of the sequences by allowing them to allocate accounts.
	for _, sequence := range sequences {
		sequence.Init(ctx, manager.conn, manager.AllocateAccounts, r, opts.useFeeGrant)
	}

	// Generate the allotted accounts on chain by sending them sufficient funds
	if err := manager.GenerateAccounts(ctx); err != nil {
		return err
	}

	errCh := make(chan error, len(sequences))

	// Spin up a task group to run each of the sequences concurrently.
	for idx, sequence := range sequences {
		go func(seqID int, sequence Sequence, errCh chan<- error) {
			opNum := 0
			r := rand.New(rand.NewSource(opts.seed))
			// each sequence loops through the next set of operations, the new messages are then
			// submitted on chain
			for {
				ops, err := sequence.Next(ctx, manager.conn, r)
				if err != nil {
					errCh <- fmt.Errorf("sequence %d: %w", seqID, err)
					return
				}

				// Submit the messages to the chain.
				if err := manager.Submit(ctx, ops); err != nil {
					errCh <- fmt.Errorf("sequence %d: %w", seqID, err)
					return
				}
				opNum++
			}
		}(idx, sequence, errCh)
	}

	var finalErr error
	for i := 0; i < len(sequences); i++ {
		err := <-errCh
		if err == nil { // should never happen
			continue
		}
		if errors.Is(err, ErrEndOfSequence) {
			logger.Info().Err(err).Msg("sequence terminated")
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		logger.Error().Err(err).Msg("sequence failed")
		finalErr = err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	return finalErr
}

type Options struct {
	seed           int64
	masterAcc      string
	pollTime       time.Duration
	useFeeGrant    bool
	suppressLogger bool
}

func (o *Options) Fill() {
	if o.seed == 0 {
		o.seed = DefaultSeed
	}
	if o.pollTime == 0 {
		o.pollTime = user.DefaultPollTime
	}
}

func DefaultOptions() *Options {
	opts := &Options{}
	opts.Fill()
	return opts
}

func (o *Options) SuppressLogs() *Options {
	o.suppressLogger = true
	return o
}

func (o *Options) UseFeeGrant() *Options {
	o.useFeeGrant = true
	return o
}

func (o *Options) SpecifyMasterAccount(name string) *Options {
	o.masterAcc = name
	return o
}

func (o *Options) WithSeed(seed int64) *Options {
	o.seed = seed
	return o
}

func (o *Options) WithPollTime(pollTime time.Duration) *Options {
	o.pollTime = pollTime
	return o
}
