// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const probeDestination = "/cetirun-probe/http-probe"

func install(destination string) error {
	if destination != probeDestination {
		return errors.New("probe install destination is invalid")
	}
	source, err := os.Open(os.Args[0])
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	return errors.Join(copyErr, target.Close())
}

func probe(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return errors.New("probe URL must be an HTTP numeric loopback endpoint")
	}
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if portErr != nil || port == 0 || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" ||
		!strings.HasPrefix(u.EscapedPath(), "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("probe URL must be an HTTP numeric loopback endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("loopback health endpoint returned status %d", response.StatusCode)
	}
	return nil
}

func run(arguments []string) error {
	if len(arguments) == 2 && arguments[0] == "install" {
		return install(arguments[1])
	}
	flags := flag.NewFlagSet("loopback-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	endpoint := flags.String("url", "", "")
	if err := flags.Parse(arguments); err != nil || *endpoint == "" || flags.NArg() != 0 {
		return errors.New("usage: http-probe --url http://127.0.0.1:PORT/PATH")
	}
	return probe(*endpoint)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
