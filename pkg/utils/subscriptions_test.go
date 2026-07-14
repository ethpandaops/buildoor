package utils

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDispatcherSubscribeAndFire(t *testing.T) {
	tests := []struct {
		name     string
		blocking bool
	}{
		{name: "non-blocking subscription receives fired events", blocking: false},
		{name: "blocking subscription receives fired events", blocking: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Dispatcher[int]{}
			sub := d.Subscribe(4, tt.blocking)

			d.Fire(42)

			select {
			case v := <-sub.Channel():
				require.Equal(t, 42, v)
			case <-time.After(time.Second):
				t.Fatal("expected event was not delivered")
			}
		})
	}
}

func TestDispatcherUnsubscribeRemovesSubscription(t *testing.T) {
	d := &Dispatcher[int]{}
	sub := d.Subscribe(4, false)

	d.Fire(1)
	require.Equal(t, 1, <-sub.Channel())

	sub.Unsubscribe()

	d.Fire(2)

	select {
	case v := <-sub.Channel():
		t.Fatalf("received event %d after unsubscribe", v)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatcherUnsubscribeKeepsOtherSubscriptions(t *testing.T) {
	d := &Dispatcher[int]{}
	sub1 := d.Subscribe(4, false)
	sub2 := d.Subscribe(4, false)
	sub3 := d.Subscribe(4, false)

	sub2.Unsubscribe()

	d.Fire(7)

	require.Equal(t, 7, <-sub1.Channel())
	require.Equal(t, 7, <-sub3.Channel())

	select {
	case <-sub2.Channel():
		t.Fatal("unsubscribed channel received an event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatcherDoubleUnsubscribeIsSafe(t *testing.T) {
	d := &Dispatcher[int]{}
	sub1 := d.Subscribe(1, false)
	sub2 := d.Subscribe(1, false)

	sub1.Unsubscribe()
	sub1.Unsubscribe()

	d.Fire(9)
	require.Equal(t, 9, <-sub2.Channel())
}

func TestSubscriptionZeroValueUnsubscribe(t *testing.T) {
	sub := &Subscription[int]{}
	sub.Unsubscribe()
}

func TestDispatcherNonBlockingDropsOnFullBuffer(t *testing.T) {
	d := &Dispatcher[int]{}
	sub := d.Subscribe(1, false)

	d.Fire(1)
	d.Fire(2)

	require.Equal(t, 1, <-sub.Channel())

	select {
	case v := <-sub.Channel():
		t.Fatalf("expected second event to be dropped, got %d", v)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatcherBlockingWaitsForConsumer(t *testing.T) {
	d := &Dispatcher[int]{}
	sub := d.Subscribe(1, true)

	fired := make(chan struct{})

	go func() {
		defer close(fired)

		d.Fire(1)
		d.Fire(2)
	}()

	require.Equal(t, 1, <-sub.Channel())
	require.Equal(t, 2, <-sub.Channel())

	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("blocking fire did not complete after events were consumed")
	}
}

func TestDispatcherConcurrentSubscribeUnsubscribeFire(t *testing.T) {
	for _, workers := range []int{1, 2, 8} {
		t.Run(fmt.Sprintf("workers-%d", workers), func(t *testing.T) {
			d := &Dispatcher[int]{}

			var wg sync.WaitGroup

			for range workers {
				wg.Go(func() {
					for i := range 100 {
						sub := d.Subscribe(4, false)

						d.Fire(i)

						// Drain whatever arrived before unsubscribing.
						for drained := false; !drained; {
							select {
							case <-sub.Channel():
							default:
								drained = true
							}
						}

						sub.Unsubscribe()
					}
				})
			}

			wg.Wait()
		})
	}
}
