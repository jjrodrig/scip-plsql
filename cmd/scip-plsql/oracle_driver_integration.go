//go:build integration

package main

import _ "github.com/sijms/go-ora/v3"

func init() {
	oracleIntegrationEnabled = true
}
