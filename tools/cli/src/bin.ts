#!/usr/bin/env bun
import { runCli } from './cli';

process.exitCode = await runCli(Bun.argv.slice(2));
