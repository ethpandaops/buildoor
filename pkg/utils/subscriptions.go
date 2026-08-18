package utils

import "sync"

type Subscription[T any] struct {
	channel    chan T
	blocking   bool
	dispatcher *Dispatcher[T]
}

type Dispatcher[T any] struct {
	mutex         sync.Mutex
	subscriptions []*Subscription[T]
}

func (d *Dispatcher[T]) Subscribe(capacity int, blocking bool) *Subscription[T] {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	subscription := &Subscription[T]{
		channel:    make(chan T, capacity),
		blocking:   blocking,
		dispatcher: d,
	}
	d.subscriptions = append(d.subscriptions, subscription)

	return subscription
}

func (s *Subscription[T]) Unsubscribe() {
	if s.dispatcher == nil {
		return
	}

	s.dispatcher.Unsubscribe(s)
}

func (s *Subscription[T]) Channel() <-chan T {
	return s.channel
}

func (d *Dispatcher[T]) Unsubscribe(subscription *Subscription[T]) {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	count := len(d.subscriptions)

	for i, s := range d.subscriptions {
		if s == subscription {
			if i < count-1 {
				d.subscriptions[i] = d.subscriptions[count-1]
			}

			d.subscriptions = d.subscriptions[:count-1]

			return
		}
	}
}

// Fire delivers data to every current subscriber. The subscriber list is
// snapshotted under the lock and released before any send: a blocking
// subscriber (see Subscribe) intentionally applies back-pressure by blocking
// the send until its consumer catches up, and doing that while still holding
// the lock would freeze Fire/Subscribe/Unsubscribe for every other
// subscriber of this dispatcher — including unrelated ones — for as long as
// the slow consumer stalls.
func (d *Dispatcher[T]) Fire(data T) {
	d.mutex.Lock()
	subs := make([]*Subscription[T], len(d.subscriptions))
	copy(subs, d.subscriptions)
	d.mutex.Unlock()

	for _, s := range subs {
		if s.blocking {
			s.channel <- data
		} else {
			select {
			case s.channel <- data:
			default:
			}
		}
	}
}
