package main

import (
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
	"interrato.dev/can/internal/can"
)

func main() {
	f, err := os.Create("log.txt")
	if err != nil {
		slog.Error("Failed to create log file", "err", err)
		os.Exit(1)
	}
	defer f.Close()
	w := io.MultiWriter(os.Stderr, f)
	slog.SetDefault(slog.New(
		tint.NewHandler(w, &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: time.TimeOnly,
		}),
	))

	bitrate := 500
	bus := can.NewBus(bitrate)

	victim, err := can.NewNode(bus, 0x00) // 000 0000 0000
	if err != nil {
		slog.Error("Failed to create victim node", "err", err)
		os.Exit(1)
	}
	adversary, err := can.NewNode(bus, 0x00) // 000 0000 0000
	if err != nil {
		slog.Error("Failed to create adversary node", "err", err)
		os.Exit(1)
	}

	delay := 5 * time.Second
	victim.SetBehavior(func() {
		for {
			victim.Enqueue(0x12, 0x34) // 0001 0010 0011 0100
			time.Sleep(delay)
		}
	})
	adversary.SetBehavior(func() {
		for {
			adversary.Enqueue(0x12, 0x14) // 0001 0010 0001 0100
			time.Sleep(delay)
		}
	})

	victim.SetBusOffFunc(func() {
		slog.Info("Bus-off attack simulation done")
		os.Exit(0)
	})

	bus.Activate()
}
