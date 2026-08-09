package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

var coinRe = regexp.MustCompile(`^(\d+)([a-zA-Z][a-zA-Z0-9/]*)$`)

func parseCoin(s string) (sdk.Coin, error) {
	s = strings.TrimSpace(s)
	m := coinRe.FindStringSubmatch(s)
	if m == nil {
		return sdk.Coin{}, fmt.Errorf("invalid coin %q (want e.g. 1000000uvault or 1000ibc/ATOM)", s)
	}
	amt, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return sdk.Coin{}, err
	}
	return sdk.NewCoin(m[2], sdkmath.NewInt(amt)), nil
}

func parseCoins(s string) (sdk.Coins, error) {
	parts := strings.Split(s, ",")
	coins := sdk.NewCoins()
	for _, p := range parts {
		c, err := parseCoin(p)
		if err != nil {
			return nil, err
		}
		coins = coins.Add(c)
	}
	return coins, nil
}

func mustCoins(s string) sdk.Coins {
	c, err := parseCoins(s)
	if err != nil {
		panic(err)
	}
	return c
}
