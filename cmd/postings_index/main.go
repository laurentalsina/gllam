package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	corpusPath := flag.String("corpus", "./bench/memarena/corpus_sessions.jsonl", "Path to corpus file")
	outputPath := flag.String("output", "", "Optional path to save serialized postings index JSON")
	queryPhrase := flag.String("phrase", "", "Search phrase to query using the index")
	queryProxTermA := flag.String("prox-a", "", "First term for proximity search")
	queryProxTermB := flag.String("prox-b", "", "Second term for proximity search")
	queryProxDist := flag.Int("prox-dist", 5, "Max distance for proximity search")

	flag.Parse()

	if *corpusPath == "" {
		fmt.Println("Please provide a valid corpus path via -corpus")
		os.Exit(1)
	}

	fmt.Printf("Building inverted postings index from %s...\n", *corpusPath)
	startTime := time.Now()
	index, err := engine.BuildInvertedIndex(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build postings index: %v\n", err)
		os.Exit(1)
	}
	elapsed := time.Since(startTime)

	fmt.Printf("Successfully indexed %d utterances and %d distinct terms in %v.\n",
		len(index.Utterances), len(index.Postings), elapsed.Round(time.Millisecond))

	if *outputPath != "" {
		fmt.Printf("Writing postings index to %s...\n", *outputPath)
		data, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal index: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outputPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write output file: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Index saved successfully.")
	}

	if *queryPhrase != "" {
		fmt.Printf("Searching for phrase: %q...\n", *queryPhrase)
		results := index.PhraseSearch(*queryPhrase)
		fmt.Printf("Found %d matches:\n", len(results))
		for i, id := range results {
			if i >= 10 {
				fmt.Println("... (showing first 10)")
				break
			}
			utt := index.Utterances[id]
			fmt.Printf("- [%s] (Line %d, Bytes %d-%d) %s: %s\n",
				id, utt.LineNumber, utt.StartByte, utt.EndByte, utt.SpeakerID, utt.Text)
		}
	}

	if *queryProxTermA != "" && *queryProxTermB != "" {
		fmt.Printf("Searching for proximity between %q and %q (max distance %d)...\n",
			*queryProxTermA, *queryProxTermB, *queryProxDist)
		results := index.ProximitySearch(*queryProxTermA, *queryProxTermB, *queryProxDist)
		fmt.Printf("Found %d matches:\n", len(results))
		for i, id := range results {
			if i >= 10 {
				fmt.Println("... (showing first 10)")
				break
			}
			utt := index.Utterances[id]
			fmt.Printf("- [%s] (Line %d, Bytes %d-%d) %s: %s\n",
				id, utt.LineNumber, utt.StartByte, utt.EndByte, utt.SpeakerID, utt.Text)
		}
	}
}
