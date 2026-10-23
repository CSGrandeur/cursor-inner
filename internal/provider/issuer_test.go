package provider

import (
	"testing"

	"cursor-inner/internal/config"
)

func TestStripForeignReasoning(t *testing.T) {
	a := config.Model{Type: "anthropic", BaseURL: "https://A.example/v1/", Model: "m"}
	b := config.Model{Type: "anthropic", BaseURL: "https://b.example/v1", Model: "m"}
	if ReasoningIssuer(a) != ReasoningIssuer(config.Model{Type: "anthropic", BaseURL: "https://a.example/v1", Model: "m", APIKey: "other"}) {
		t.Fatal("issuer must ignore key and trailing slash / case of the URL")
	}
	msgs := []Message{
		{Role: "assistant", Reasoning: "x", ReasoningSignature: "SA", ReasoningBy: ReasoningIssuer(a)},
		{Role: "assistant", Reasoning: "y", ReasoningSignature: "LEGACY"},
	}
	got := StripForeignReasoning(b, msgs)
	if got[0].ReasoningSignature != "" || got[0].Reasoning != "x" {
		t.Fatalf("foreign signature kept or text lost: %+v", got[0])
	}
	if got[1].ReasoningSignature != "LEGACY" {
		t.Fatal("unstamped legacy message must stay as is")
	}
	if same := StripForeignReasoning(a, msgs); same[0].ReasoningSignature != "SA" {
		t.Fatal("own signature must be kept")
	}
	if msgs[0].ReasoningSignature != "SA" {
		t.Fatal("input mutated")
	}
}
