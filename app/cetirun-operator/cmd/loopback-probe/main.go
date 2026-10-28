// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// loopback-probe is the platform-owned HTTP probe for portable Custom
// workloads. It deliberately accepts only a numeric loopback URL so a
// compromised workload cannot turn the kubelet probe into a network client.
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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const timeout = 2 * time.Second

const probeDestination = "/cetirun-probe/http-probe"

func install(destination string) error {
	if destination != probeDestination || !filepath.IsAbs(destination) || filepath.Base(destination) != "http-probe" {
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
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func probe(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return errors.New("probe URL must be an HTTP numeric loopback endpoint")
	}
	port, portErr := strconv.Atoi(u.Port())
	if portErr != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" ||
		port < 1 || port > 65535 || !strings.HasPrefix(u.EscapedPath(), "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("probe URL must be an HTTP numeric loopback endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
