package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/cocoyes/zhizhi-agent-runtime/adapter/speech/doubao"
	"github.com/cocoyes/zhizhi-agent-runtime/speech"
	"github.com/cocoyes/zhizhi-agent-runtime/tool"
)

type timeInput struct {
	Timezone string `json:"timezone" jsonschema:"required"`
}

type timeOutput struct {
	Timezone string `json:"timezone"`
	Time     string `json:"time"`
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: go run ./examples/12-doubao-realtime input.pcm output.pcm")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	clock := tool.Func("clock.now", "Return the current time in a timezone", func(_ context.Context, input timeInput) (timeOutput, error) {
		location, err := time.LoadLocation(input.Timezone)
		if err != nil {
			return timeOutput{}, err
		}
		return timeOutput{Timezone: input.Timezone, Time: time.Now().In(location).Format(time.RFC3339)}, nil
	})
	toolSet, err := speech.RegistryToolSet(tool.NewRegistry(clock), nil)
	if err != nil {
		log.Fatal(err)
	}
	client, err := doubao.New(doubao.Config{APIKey: os.Getenv("DOUBAO_REALTIME_API_KEY")})
	if err != nil {
		log.Fatal(err)
	}
	config := doubao.DefaultSession("你是一个简洁友好的语音助手。", os.Getenv("DOUBAO_REALTIME_VOICE"))
	config.Tools = toolSet.Definitions
	session, err := client.Open(ctx, config)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close(context.Background())

	input, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer input.Close()
	go sendPCM(ctx, session, input)

	output, err := os.Create(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	defer output.Close()
	for {
		event, err := session.Recv(ctx)
		if err != nil {
			if speech.IsEnd(err) || errors.Is(err, context.Canceled) {
				return
			}
			log.Fatal(err)
		}
		switch event.Type {
		case speech.EventTranscriptionDone:
			fmt.Println("user:", event.Transcript)
		case speech.EventTextDone:
			fmt.Println("assistant:", event.Text)
		case speech.EventAudioDelta:
			if _, err := output.Write(event.Audio); err != nil {
				log.Fatal(err)
			}
		case speech.EventFunctionCalls:
			if err := session.ExecuteFunctionCalls(ctx, toolSet.Executor, event.FunctionCalls...); err != nil {
				log.Printf("function call: %v", err)
			}
		case speech.EventAudioDone:
			return
		case speech.EventError:
			log.Fatal(event.ProviderError)
		}
	}
}

func sendPCM(ctx context.Context, session speech.Session, input io.Reader) {
	frame := make([]byte, 640)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		n, err := io.ReadFull(input, frame)
		if n > 0 {
			<-ticker.C
			if sendErr := session.SendAudio(ctx, frame[:n]); sendErr != nil {
				return
			}
		}
		if err != nil {
			_ = session.CommitAudio(ctx)
			return
		}
	}
}
