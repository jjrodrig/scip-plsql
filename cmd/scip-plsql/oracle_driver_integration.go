//go:build integration

package main

import _ "github.com/sijms/go-ora/v2"

func init() {
	oracleIntegrationEnabled = true
}
