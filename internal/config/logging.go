// Copyright 2021 Adam Chalkley
//
// https://github.com/atc0005/check-vmware
//
// Licensed under the MIT License. See LICENSE file in the project root for
// full license information.

package config

import (
	"fmt"
	"os"

	"github.com/rs/zerolog"
)

const (

	// LogLevelDisabled maps to zerolog.Disabled logging level
	LogLevelDisabled string = "disabled"

	// LogLevelPanic maps to zerolog.PanicLevel logging level
	LogLevelPanic string = "panic"

	// LogLevelFatal maps to zerolog.FatalLevel logging level
	LogLevelFatal string = "fatal"

	// LogLevelError maps to zerolog.ErrorLevel logging level
	LogLevelError string = "error"

	// LogLevelWarn maps to zerolog.WarnLevel logging level
	LogLevelWarn string = "warn"

	// LogLevelInfo maps to zerolog.InfoLevel logging level
	LogLevelInfo string = "info"

	// LogLevelDebug maps to zerolog.DebugLevel logging level
	LogLevelDebug string = "debug"

	// LogLevelTrace maps to zerolog.TraceLevel logging level
	LogLevelTrace string = "trace"
)

// loggingLevels is a map of string to zerolog.Level created in an effort to
// keep from repeating ourselves
var loggingLevels = make(map[string]zerolog.Level)

func init() {

	// https://stackoverflow.com/a/59426901
	// syntax error: non-declaration statement outside function body
	//
	// Workaround: Use init() to setup this map for later reference
	loggingLevels[LogLevelDisabled] = zerolog.Disabled
	loggingLevels[LogLevelPanic] = zerolog.PanicLevel
	loggingLevels[LogLevelFatal] = zerolog.FatalLevel
	loggingLevels[LogLevelError] = zerolog.ErrorLevel
	loggingLevels[LogLevelWarn] = zerolog.WarnLevel
	loggingLevels[LogLevelInfo] = zerolog.InfoLevel
	loggingLevels[LogLevelDebug] = zerolog.DebugLevel
	loggingLevels[LogLevelTrace] = zerolog.TraceLevel
}

// setLoggingLevel applies the requested logging level to filter out messages
// with a lower level than the one configured.
func setLoggingLevel(logLevel string) error {

	switch logLevel {
	case LogLevelDisabled:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelDisabled])
	case LogLevelPanic:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelPanic])
	case LogLevelFatal:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelFatal])
	case LogLevelError:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelError])
	case LogLevelWarn:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelWarn])
	case LogLevelInfo:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelInfo])
	case LogLevelDebug:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelDebug])
	case LogLevelTrace:
		zerolog.SetGlobalLevel(loggingLevels[LogLevelTrace])
	default:
		return fmt.Errorf("invalid option provided: %v", logLevel)
	}

	// signal that a case was triggered as expected
	return nil

}

// setupLogging is responsible for configuring logging settings for this
// application
func (c *Config) setupLogging(pluginType PluginType) error {

	var appDescription string

	switch {
	case pluginType.SnapshotsAge:
		appDescription = PluginTypeSnapshotsAge

	case pluginType.SnapshotsSize:
		appDescription = PluginTypeSnapshotsSize

	case pluginType.DatastoresSize:
		appDescription = PluginTypeDatastoresSize

	case pluginType.ResourcePoolsMemory:
		appDescription = PluginTypeResourcePoolsMemory

	case pluginType.VirtualCPUsAllocation:
		appDescription = PluginTypeVirtualCPUsAllocation

	case pluginType.Tools:
		appDescription = PluginTypeTools

	}

	// We set some common fields here so that we don't have to repeat them
	// explicitly later and then set additional fields while processing each
	// email account. This approach is intended to help standardize the log
	// messages to make them easier to search through later when
	// troubleshooting. Logging goes to stderr to prevent mixing in with
	// stdout output intended for the Nagios console.
	c.Log = zerolog.New(os.Stderr).With().Timestamp().Caller().
		Str("version", Version()).
		Str("logging_level", c.LoggingLevel).
		Str("plugin_type", appDescription).
		Str("connection_timeout", c.Timeout().String()).
		Str("username", c.Username).
		Str("user_domain", c.Domain).
		Bool("trust_cert", c.TrustCert).
		Str("server", c.Server).
		Int("port", c.Port).
		// Int("age_warning", c.AgeWarning).
		// Int("age_critical", c.AgeCritical).
		Logger()

	if err := setLoggingLevel(c.LoggingLevel); err != nil {
		return err
	}

	return nil

}