package main

import (
	"fmt"
	"github.com/google/uuid"
	stan "github.com/nats-io/go-nats-streaming"
	"golang.org/x/sync/errgroup"
	"log"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"
)

type Identification struct {
	sync.Mutex
	number int
}

func NewIdentification() *Identification {
	return &Identification{}
}

func (i *Identification) NextNumber() string {
	i.Lock()
	defer i.Unlock()
	i.number++
	return strconv.Itoa(i.number)
}

func sender(stop <-chan struct{}) error {
	sc, err := stan.Connect(
		"test-cluster",
		"client-sender",
		stan.NatsURL(os.Getenv("NATS_STREAMING_URL")),
	)
	if err != nil {
		return err
	}
	identification := NewIdentification()
	for {
		select {
		case <-stop:
			_ = sc.Close()
			return nil
		default:
			clickUuid := strconv.Itoa(rand.Intn(999))
			err = sc.Publish("subject", []byte(clickUuid))
			if err != nil {
				return err
			}
			number := identification.NextNumber()
			err = sc.Publish(clickUuid, []byte(number+":"+clickUuid+":"+time.Now().String()))
			if err != nil {
				return err
			}
		}
	}
}

func readers(stop <-chan struct{}) error {
	sc, err := stan.Connect(
		"test-cluster",
		uuid.New().String(),
		stan.NatsURL(os.Getenv("NATS_STREAMING_URL")),
	)
	if err != nil {
		return err
	}
	subjects := make(chan string, 500)
	sub, err := sc.QueueSubscribe("subject", "a", handleSubject(subjects),
		stan.DeliverAllAvailable(),
		stan.SetManualAckMode(),
		stan.AckWait(30*time.Second),
	)
	if err != nil {
		return err
	}
	var g errgroup.Group
	chanErrors := make(chan error)
	g.Go(func() error {
		<-stop
		return nil
	})
	go func() { chanErrors <- g.Wait() }()
	for {
		select {
		case err := <-chanErrors:
			if err != nil {
				_ = sub.Close()
				_ = sc.Close()
				return err
			}

			if err := sub.Close(); err != nil {
				return err
			}
			if err := sc.Close(); err != nil {
				return err
			}
		case subject := <-subjects:
			g.Go(func() error {
				sub, err := sc.QueueSubscribe(subject, "b", func(m *stan.Msg) {
					err := m.Ack()
					if err != nil {
						log.Println(err)
					}
					fmt.Println(string(m.Data))
				},
					stan.DeliverAllAvailable(),
					stan.SetManualAckMode(),
					stan.AckWait(30*time.Second),
				)
				if err != nil {
					return err
				}
				time.Sleep(2 * time.Second)
				if err := sub.Close(); err != nil {
					return err
				}
				return nil
			})
		}
	}
}

func handleSubject(subject chan<- string) func(m *stan.Msg) {
	return func(m *stan.Msg) {
		subject <- string(m.Data)
		if err := m.Ack(); err != nil {
			log.Println(err)
			return
		}
	}

}

func main() {
	done := make(chan error, 2)
	stop := make(chan struct{})
	go func() {
		done <- sender(stop)
	}()
	go func() {
		done <- readers(stop)
	}()

	var stopped bool
	for i := 0; i < cap(done); i++ {
		select {
		case <-time.After(99 * time.Second):
			if !stopped {
				stopped = true
				close(stop)
			}
		case err := <-done:
			if err != nil {
				log.Printf("error: %v\n", err)
			}
		}
		if !stopped {
			stopped = true
			close(stop)
		}
	}
}
