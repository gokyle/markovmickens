// Copyright 2011 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Generating random text: a Markov chain algorithm

Based on the program presented in the "Design and Implementation" chapter
of The Practice of Programming (Kernighan and Pike, Addison-Wesley 1999).
See also Computer Recreations, Scientific American 260, 122 - 125 (1989).

A Markov chain algorithm generates text by creating a statistical model of
potential textual suffixes for a given prefix. Consider this text:

	I am not a number! I am a free man!

Our Markov chain algorithm would arrange this text into this set of prefixes
and suffixes, or "chain": (This table assumes a prefix length of two words.)

	Prefix       Suffix

	"" ""        I
	"" I         am
	I am         a
	I am         not
	a free       man!
	am a         free
	am not       a
	a number!    I
	number! I    am
	not a        number!

To generate text using this table we select an initial prefix ("I am", for
example), choose one of the suffixes associated with that prefix at random
with probability determined by the input statistics ("a"),
and then create a new prefix by removing the first word from the prefix
and appending the suffix (making the new prefix is "am a"). Repeat this process
until we can't find any suffixes for the current prefix or we exceed the word
limit. (The word limit is necessary as the chain table may contain cycles.)

Our version of this program reads text from standard input, parsing it into a
Markov chain, and writes generated text to standard output.
The prefix and output lengths can be specified using the -prefix and -words
flags on the command-line.
*/
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/kurrik/oauth1a"
	"github.com/kurrik/twittergo"
	"io"
	"io/ioutil"
	"log"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Prefix is a Markov chain prefix of one or more words.
type Prefix []string

// String returns the Prefix as a string (for use as a map key).
func (p Prefix) String() string {
	return strings.Join(p, " ")
}

// Shift removes the first word from the Prefix and appends the given word.
func (p Prefix) Shift(word string) {
	copy(p, p[1:])
	p[len(p)-1] = word
}

// Chain contains a map ("chain") of prefixes to a list of suffixes.
// A prefix is a string of prefixLen words joined with spaces.
// A suffix is a single word. A prefix can have multiple suffixes.
type Chain struct {
	chain     map[string][]string
	words     map[string]bool
	prefixLen int
}

// NewChain returns a new Chain with prefixes of prefixLen words.
func NewChain(prefixLen int) *Chain {
	return &Chain{make(map[string][]string), make(map[string]bool), prefixLen}
}

// Build reads text from the provided Reader and
// parses it into prefixes and suffixes that are stored in Chain.
func (c *Chain) Build(r io.Reader) {
	br := bufio.NewReader(r)
	p := make(Prefix, c.prefixLen)
	for {
		var s string
		if _, err := fmt.Fscan(br, &s); err != nil {
			break
		}
		c.words[s] = true
		key := p.String()
		c.chain[key] = append(c.chain[key], s)
		p.Shift(s)
	}
}

// Generate returns a string of at most n words generated from Chain.
func (c *Chain) Generate(n int) string {
	p := make(Prefix, c.prefixLen)
	var words []string
	for i := 0; i < n; i++ {
		choices := c.chain[p.String()]
		if len(choices) == 0 {
			break
		}
		next := choices[mrand.Intn(len(choices))]
		words = append(words, next)
		p.Shift(next)
	}
	return strings.Join(words, " ")
}

func (c *Chain) Words() []string {
	var ss []string
	for k, _ := range c.words {
		ss = append(ss, k)
	}
	return ss
}

func seed() {
	var seed = make([]byte, 8)
	_, err := io.ReadFull(rand.Reader, seed)
	if err != nil {
		fmt.Printf("FATAL: %v\n", err)
	}

	seedVal := int64(binary.BigEndian.Uint64(seed))
	log.Printf("seed value: %d", seedVal)
	mrand.Seed(seedVal)
}

func splitForTweet(in string) []string {
	var tweets []string
	var words = strings.Split(in, " ")

	for {
		var tweet = ""
		for {
			tweet += words[0]
			words = words[1:]
			if len(words) == 0 {
				tweets = append(tweets, tweet)
				break
			} else if len(tweet)+len(words[0]) > 138 {
				tweets = append(tweets, tweet)
				break
			}
			tweet += " "
		}
		if len(words) == 0 {
			break
		}
	}
	return tweets
}

func LoadCredentials() (client *twittergo.Client, err error) {
	config := &oauth1a.ClientConfig{
		ConsumerKey:    os.Getenv("CONSUMER_KEY"),
		ConsumerSecret: os.Getenv("CONSUMER_SECRET"),
	}
	user := oauth1a.NewAuthorizedConfig(os.Getenv("API_KEY"), os.Getenv("API_SECRET"))
	client = twittergo.NewClient(config, user)
	return
}

func postTweet(status string) error {
	var (
		err    error
		client *twittergo.Client
		req    *http.Request
		resp   *twittergo.APIResponse
		tweet  *twittergo.Tweet
	)
	client, err = LoadCredentials()
	if err != nil {
		fmt.Printf("Could not parse CREDENTIALS file: %v\n", err)
		os.Exit(1)
	}
	data := url.Values{}
	data.Set("status", status)
	body := strings.NewReader(data.Encode())
	req, err = http.NewRequest("POST", "/1.1/statuses/update.json", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = client.SendRequest(req)
	if err != nil {
		return err
	}
	tweet = &twittergo.Tweet{}
	err = resp.Parse(tweet)
	if err != nil {
		if rle, ok := err.(twittergo.RateLimitError); ok {
			fmt.Printf("Rate limited, reset at %v\n", rle.Reset)
		} else if errs, ok := err.(twittergo.Errors); ok {
			for i, val := range errs.Errors() {
				fmt.Printf("Error #%v - ", i+1)
				fmt.Printf("Code: %v ", val.Code())
				fmt.Printf("Msg: %v\n", val.Message())
			}
		} else {
			fmt.Printf("Problem parsing response: %v\n", err)
		}
	}
	return err
}

func httpTickle(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("OK"))
}

func server() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := ":" + port
	// http.HandleFunc("/reload", httpReload)
	http.HandleFunc("/tickle", httpTickle)
	log.Println("starting server on", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

var sentRegexp = regexp.MustCompile(`^(.+[.!?])`)

func main() {
	// Register command-line flags.
	numWords := flag.Int("words", 30, "maximum number of words to print")
	prefixLen := flag.Int("prefix", 2, "prefix length in words")

	flag.Parse()                      // Parse command-line flags.
	mrand.Seed(time.Now().UnixNano()) // Seed the random number generator.

	c := NewChain(*prefixLen) // Initialize a new Chain.

	in, err := ioutil.ReadFile("parsed-mickens.txt")
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}
	in = bytes.TrimSpace(in)
	inSlice := bytes.Split(in, []byte{0xa})

	for i := 0; i < len(inSlice); i++ {
		buf := bytes.NewBuffer(inSlice[i])
		c.Build(buf) // Build chains from standard input.
	}

	go server()
	go func() {
		for {
			delay := time.Duration(mrand.Int63n(7200) + 3600)
			delay *= 1000000000

			var tweet string
			for {
				tweet = c.Generate(*numWords) // Generate text.
				tweet = strings.TrimSpace(sentRegexp.FindString(tweet))
				if len(tweet) != 0 {
					break
				}
			}

			tweets := splitForTweet(tweet)
			log.Printf("posting new status")
			for i := 0; i < len(tweets); i++ {
				log.Printf("tweet %d / %d", i, len(tweets)-1)
				err := postTweet(tweets[i])
				if err != nil {
					fmt.Printf("ERROR: %v\n", err)
				}
				<-time.After(250 * time.Millisecond)
			}
			log.Println("OK")
			log.Printf("delay for %s", delay.String())
			<-time.After(delay)
		}
	}()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Kill, os.Interrupt, syscall.SIGTERM)
	<-sigc
	log.Println("shutting down.")
}
