package bliss_test

import (
	"context"
	"log"

	"github.com/viggfred/blissble/pkg/bliss"
)

// Example shows the typical control flow: connect (which scans, logs in and
// subscribes to status), then issue commands.
func Example() {
	blind := bliss.New(bliss.Config{MACAddress: "AA:BB:CC:DD:EE:01"})
	blind.OnEvent(func(ev bliss.Event) {
		if ev.Type == bliss.EventStatus {
			log.Printf("position=%d battery=%s", ev.Position, ev.Battery)
		}
	})

	if err := blind.Connect(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer blind.Disconnect()

	_ = blind.Open()          // raise
	_ = blind.SetPosition(50) // go to 50%
	_ = blind.GoToFavorite()  // or a saved favorite
	_ = blind.Stop()
}
