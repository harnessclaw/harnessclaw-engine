const assert = require('node:assert/strict')
const { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } = require('node:fs')
const { tmpdir } = require('node:os')
const { join } = require('node:path')
const test = require('node:test')

const {
  agentBrowserFileName,
  createRuntimePlan,
  directAgentBrowserAssetUrl,
  engineFileName,
  normalizeRuntimeArch,
  normalizeRuntimePlatform,
  parseArgs,
  prepareRuntime,
} = require('./prepare-runtime.cjs')

test('normalizes runtime platform and arch names', () => {
  assert.equal(normalizeRuntimePlatform('mac'), 'darwin')
  assert.equal(normalizeRuntimePlatform('win32'), 'windows')
  assert.equal(normalizeRuntimeArch('amd64'), 'x64')
  assert.equal(normalizeRuntimeArch('aarch64'), 'arm64')
})

test('uses Electron-compatible engine names and upstream agent-browser names', () => {
  assert.equal(engineFileName('darwin', 'arm64'), 'harnessclaw-engine-darwin-arm64')
  assert.equal(engineFileName('windows', 'x64'), 'harnessclaw-engine-windows-x64.exe')
  assert.equal(agentBrowserFileName('darwin', 'x64'), 'agent-browser-darwin-x64')
  assert.equal(agentBrowserFileName('windows', 'x64'), 'agent-browser-win32-x64.exe')
})

test('creates a local prepare-runtime plan without building engine by default', () => {
  const plan = createRuntimePlan({
    argv: ['--platform', 'darwin', '--arch', 'arm64', '--output-dir', '.runtime/bin'],
    env: { AGENT_BROWSER_VERSION: '0.27.1' },
  })

  assert.equal(plan.includeEngine, false)
  assert.equal(plan.platform, 'darwin')
  assert.equal(plan.arch, 'arm64')
  assert.equal(plan.outputDir, join(__dirname, '..', '.runtime', 'bin'))
  assert.equal(plan.agentBrowser.fileName, 'agent-browser-darwin-arm64')
  assert.equal(plan.engine.fileName, 'harnessclaw-engine-darwin-arm64')
})

test('archive plans include engine and produce a runtime bundle zip', () => {
  const plan = createRuntimePlan({
    argv: ['--platform', 'win', '--arch', 'amd64', '--archive-dir', 'dist'],
    env: { AGENT_BROWSER_VERSION: '0.27.1' },
  })

  assert.equal(plan.includeEngine, true)
  assert.equal(plan.bundleName, 'harnessclaw-engine-runtime-windows-x64')
  assert.equal(plan.archivePath, join(__dirname, '..', 'dist', 'harnessclaw-engine-runtime-windows-x64.zip'))
  assert.equal(plan.outputDir, join(__dirname, '..', 'dist', 'runtime', 'harnessclaw-engine-runtime-windows-x64', 'bin'))
  assert.equal(plan.manifestPath, join(__dirname, '..', 'dist', 'runtime', 'harnessclaw-engine-runtime-windows-x64', 'manifest.json'))
})

test('parses print-agent-browser-path as a read-only mode flag', () => {
  const args = parseArgs(['--output-dir', '.runtime/bin', '--print-agent-browser-path'])

  assert.equal(args.outputDir, '.runtime/bin')
  assert.equal(args.printAgentBrowserPath, true)
})

test('uses direct release asset URLs for pinned agent-browser versions', () => {
  const plan = createRuntimePlan({
    argv: ['--platform', 'darwin', '--arch', 'arm64', '--output-dir', '.runtime/bin'],
    env: { AGENT_BROWSER_VERSION: '0.27.1' },
  })

  assert.equal(
    directAgentBrowserAssetUrl(plan),
    'https://github.com/vercel-labs/agent-browser/releases/download/v0.27.1/agent-browser-darwin-arm64',
  )
})

test('reuses prepared agent-browser when output manifest matches', async () => {
  const outputDir = mkdtempSync(join(tmpdir(), 'harnessclaw-runtime-'))
  try {
    const plan = createRuntimePlan({
      argv: ['--platform', 'darwin', '--arch', 'arm64', '--output-dir', outputDir],
      env: { AGENT_BROWSER_VERSION: '0.27.1' },
    })
    mkdirSync(outputDir, { recursive: true })
    writeFileSync(plan.agentBrowser.targetPath, '#!/bin/sh\necho "agent-browser 0.27.1"\n')
    chmodSync(plan.agentBrowser.targetPath, 0o755)
    writeFileSync(join(outputDir, 'harnessclaw-runtime-manifest.json'), `${JSON.stringify({
      schema_version: 1,
      platform: 'darwin',
      arch: 'arm64',
      binaries: {
        agent_browser: {
          path: 'agent-browser-darwin-arm64',
          version: '0.27.1',
          repo: 'vercel-labs/agent-browser',
        },
      },
    }, null, 2)}\n`)

    await prepareRuntime(plan)

    assert.equal(readFileSync(plan.agentBrowser.targetPath, 'utf8'), '#!/bin/sh\necho "agent-browser 0.27.1"\n')
    assert.equal(existsSync(join(outputDir, 'harnessclaw-runtime-manifest.json')), true)
  } finally {
    rmSync(outputDir, { recursive: true, force: true })
  }
})
