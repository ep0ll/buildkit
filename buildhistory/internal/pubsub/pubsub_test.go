package pubsub

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPubsubSendReceive(t *testing.T) {
	ps := &Pubsub[int]{m: map[*Channel[int]]struct{}{}}

	sub1 := ps.Subscribe()
	sub2 := ps.Subscribe()

	ps.Send(42)

	v1 := <-sub1.Ch
	v2 := <-sub2.Ch

	require.Equal(t, 42, v1)
	require.Equal(t, 42, v2)

	sub1.Close()

	ps.Send(99)

	v2 = <-sub2.Ch
	require.Equal(t, 99, v2)

	select {
	case <-sub1.Ch:
		t.Fatal("received on closed subscriber")
	default:
	}

	sub2.Close()
}

func TestPubsubClose(t *testing.T) {
	ps := &Pubsub[string]{m: map[*Channel[string]]struct{}{}}

	sub := ps.Subscribe()
	ps.Close()

	select {
	case <-sub.Done:
	default:
		t.Fatal("subscriber done channel not closed after pubsub Close")
	}
}

func TestPubsubCloseIdempotent(t *testing.T) {
	ps := &Pubsub[int]{m: map[*Channel[int]]struct{}{}}

	sub := ps.Subscribe()
	sub.Close()
	sub.Close()
}

func TestPubsubConcurrent(t *testing.T) {
	ps := &Pubsub[int]{m: map[*Channel[int]]struct{}{}}

	const numSubs = 10
	const numMsgs = 100

	subs := make([]*Channel[int], numSubs)
	for i := range subs {
		subs[i] = ps.Subscribe()
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		for i := range numMsgs {
			ps.Send(i)
		}
	})

	received := make([][]int, numSubs)
	for i, sub := range subs {
		wg.Go(func() {
			for range numMsgs {
				v := <-sub.Ch
				received[i] = append(received[i], v)
			}
		})
	}

	wg.Wait()

	for i := range numSubs {
		require.Len(t, received[i], numMsgs)
	}

	for _, sub := range subs {
		sub.Close()
	}
}
