const { chmodSync, copyFileSync, createWriteStream, existsSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } = require('fs')
const https = require('https')
const { join, resolve, dirname } = require('path')
const { spawnSync } = require('child_process')

const repoRoot = join(__dirname, '..')
const agentBrowserVersionPath = join(repoRoot, 'runtime', 'agent-browser', 'VERSION')

function parseArgs(argv) {
  const out = {
    includeEngine: false,
    printAgentBrowserPath: false,
  }

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]
    if (arg === '--platform') {
      out.platform = argv[++i]
      continue
    }
    if (arg === '--arch') {
      out.arch = argv[++i]
      continue
    }
    if (arg === '--output-dir') {
      out.outputDir = argv[++i]
      continue
    }
    if (arg === '--archive-dir') {
      out.archiveDir = argv[++i]
      continue
    }
    if (arg === '--archive-path') {
      out.archivePath = argv[++i]
      continue
    }
    if (arg === '--bundle-root') {
      out.bundleRoot = argv[++i]
      continue
    }
    if (arg === '--manifest-path') {
      out.manifestPath = argv[++i]
      continue
    }
    if (arg === '--include-engine') {
      out.includeEngine = true
      continue
    }
    if (arg === '--print-agent-browser-path') {
      out.printAgentBrowserPath = true
      continue
    }
    throw new Error(`Unknown argument: ${arg}`)
  }

  return out
}

function normalizeRuntimePlatform(platform) {
  if (!platform) return normalizeRuntimePlatform(process.platform)
  if (platform === 'darwin' || platform === 'mac' || platform === 'macos') return 'darwin'
  if (platform === 'linux') return 'linux'
  if (platform === 'win32' || platform === 'windows' || platform === 'win') return 'windows'
  throw new Error(`Unsupported runtime platform: ${platform}`)
}

function normalizeRuntimeArch(arch) {
  if (!arch) return normalizeRuntimeArch(process.arch)
  if (arch === 'x64' || arch === 'amd64' || arch === 'x86_64') return 'x64'
  if (arch === 'arm64' || arch === 'aarch64') return 'arm64'
  throw new Error(`Unsupported runtime arch: ${arch}`)
}

function runtimePlatformToGoOS(platform) {
  return platform === 'windows' ? 'windows' : platform
}

function runtimeArchToGoArch(arch) {
  return arch === 'x64' ? 'amd64' : arch
}

function runtimePlatformToAgentBrowserPlatform(platform) {
  return platform === 'windows' ? 'win32' : platform
}

function runtimeExtension(platform) {
  return platform === 'windows' ? '.exe' : ''
}

function agentBrowserFileName(platform, arch) {
  const agentPlatform = runtimePlatformToAgentBrowserPlatform(platform)
  const extension = agentPlatform === 'win32' ? '.exe' : ''
  return `agent-browser-${agentPlatform}-${arch}${extension}`
}

function engineFileName(platform, arch) {
  return `harnessclaw-engine-${platform}-${arch}${runtimeExtension(platform)}`
}

function resolveFromRepo(value) {
  return resolve(repoRoot, value)
}

function readAgentBrowserVersion(env = process.env) {
  const override = (env.AGENT_BROWSER_VERSION || '').trim()
  if (override) return override
  if (!existsSync(agentBrowserVersionPath)) {
    throw new Error(`Missing agent-browser version lock at ${agentBrowserVersionPath}`)
  }
  const version = readFileSync(agentBrowserVersionPath, 'utf8').trim()
  if (!version) {
    throw new Error(`agent-browser version lock is empty at ${agentBrowserVersionPath}`)
  }
  return version
}

function createRuntimePlan({ argv = process.argv.slice(2), env = process.env } = {}) {
  const args = parseArgs(argv)
  const platform = normalizeRuntimePlatform(args.platform || env.HARNESSCLAW_RUNTIME_PLATFORM || env.HARNESSCLAW_ENGINE_PLATFORM || process.platform)
  const arch = normalizeRuntimeArch(args.arch || env.HARNESSCLAW_RUNTIME_ARCH || env.HARNESSCLAW_ENGINE_ARCH || process.arch)
  const bundleName = `harnessclaw-engine-runtime-${platform}-${arch}`
  const archiveRequested = Boolean(args.archiveDir || args.archivePath)
  const bundleRoot = args.bundleRoot
    ? resolveFromRepo(args.bundleRoot)
    : join(repoRoot, 'dist', 'runtime', bundleName)
  const outputDir = args.outputDir
    ? resolveFromRepo(args.outputDir)
    : archiveRequested
      ? join(bundleRoot, 'bin')
      : join(repoRoot, '.runtime', 'bin')
  const archivePath = args.archivePath
    ? resolveFromRepo(args.archivePath)
    : args.archiveDir
      ? join(resolveFromRepo(args.archiveDir), `${bundleName}.zip`)
      : ''
  const manifestPath = args.manifestPath
    ? resolveFromRepo(args.manifestPath)
    : archiveRequested
      ? join(bundleRoot, 'manifest.json')
      : ''
  const includeEngine = args.includeEngine || archiveRequested || env.HARNESSCLAW_RUNTIME_INCLUDE_ENGINE === '1'
  const agentBrowserVersion = readAgentBrowserVersion(env)

  return {
    platform,
    arch,
    bundleName,
    bundleRoot,
    outputDir,
    archivePath,
    manifestPath,
    includeEngine,
    agentBrowserVersion,
    goos: runtimePlatformToGoOS(platform),
    goarch: runtimeArchToGoArch(arch),
    engine: {
      fileName: engineFileName(platform, arch),
      targetPath: join(outputDir, engineFileName(platform, arch)),
    },
    agentBrowser: {
      fileName: agentBrowserFileName(platform, arch),
      targetPath: join(outputDir, agentBrowserFileName(platform, arch)),
      repo: env.AGENT_BROWSER_REPO || 'vercel-labs/agent-browser',
    },
  }
}

function request(url, headers, redirectCount = 0) {
  return new Promise((resolveRequest, rejectRequest) => {
    const timeoutMs = downloadTimeoutMs()
    let timeout = null
    const clearRequestTimeout = () => {
      if (timeout) {
        clearTimeout(timeout)
        timeout = null
      }
    }

    const req = https.get(url, { headers }, (response) => {
      clearRequestTimeout()
      const statusCode = response.statusCode || 0

      if ([301, 302, 303, 307, 308].includes(statusCode) && response.headers.location) {
        response.resume()
        if (redirectCount >= 5) {
          rejectRequest(new Error(`Too many redirects while requesting ${url}`))
          return
        }
        resolveRequest(request(response.headers.location, headers, redirectCount + 1))
        return
      }

      if (statusCode < 200 || statusCode >= 300) {
        const chunks = []
        response.on('data', (chunk) => chunks.push(chunk))
        response.on('end', () => {
          rejectRequest(new Error(`Request to ${url} failed: ${statusCode} ${Buffer.concat(chunks).toString('utf8').trim()}`))
        })
        return
      }

      resolveRequest(response)
    })

    req.on('error', (error) => {
      clearRequestTimeout()
      rejectRequest(error)
    })
    if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
      timeout = setTimeout(() => {
        req.destroy(new Error(`Request to ${url} timed out after ${timeoutMs}ms`))
      }, timeoutMs)
    }
  })
}

function downloadTimeoutMs() {
  const timeoutMs = Number(process.env.HARNESSCLAW_DOWNLOAD_TIMEOUT_MS || process.env.AGENT_BROWSER_DOWNLOAD_TIMEOUT_MS || 30000)
  return Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : 30000
}

function downloadTotalTimeoutMs() {
  const timeoutMs = Number(process.env.HARNESSCLAW_DOWNLOAD_TOTAL_TIMEOUT_MS || process.env.AGENT_BROWSER_DOWNLOAD_TOTAL_TIMEOUT_MS || 0)
  return Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : 0
}

async function fetchJson(url, headers) {
  const response = await request(url, headers)
  const chunks = []
  for await (const chunk of response) {
    chunks.push(chunk)
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8'))
}

async function downloadToFile(url, headers, targetPath) {
  const response = await request(url, headers)
  await new Promise((resolveDownload, rejectDownload) => {
    const fileStream = createWriteStream(targetPath)
    const timeoutMs = downloadTimeoutMs()
    const totalTimeoutMs = downloadTotalTimeoutMs()
    let inactivityTimer = null
    let totalTimer = null
    let settled = false

    const clearInactivityTimer = () => {
      if (inactivityTimer) {
        clearTimeout(inactivityTimer)
        inactivityTimer = null
      }
    }
    const clearTotalTimer = () => {
      if (totalTimer) {
        clearTimeout(totalTimer)
        totalTimer = null
      }
    }
    const failDownload = (error) => {
      if (settled) return
      settled = true
      clearInactivityTimer()
      clearTotalTimer()
      response.destroy(error)
      fileStream.destroy(error)
      rejectDownload(error)
    }
    const armInactivityTimer = () => {
      clearInactivityTimer()
      inactivityTimer = setTimeout(() => {
        failDownload(new Error(`Download from ${url} timed out after ${timeoutMs}ms without data`))
      }, timeoutMs)
    }

    armInactivityTimer()
    if (totalTimeoutMs > 0) {
      totalTimer = setTimeout(() => {
        failDownload(new Error(`Download from ${url} exceeded total timeout ${totalTimeoutMs}ms`))
      }, totalTimeoutMs)
    }
    response.on('data', armInactivityTimer)
    response.pipe(fileStream)

    response.on('error', failDownload)
    fileStream.on('error', failDownload)
    fileStream.on('finish', () => {
      fileStream.close((error) => {
        if (settled) return
        clearInactivityTimer()
        clearTotalTimer()
        if (error) {
          failDownload(error)
          return
        }
        settled = true
        resolveDownload()
      })
    })
  })
}

function agentBrowserReleaseTag(version) {
  return `v${String(version).replace(/^v/, '')}`
}

function directAgentBrowserAssetUrl(plan) {
  const version = String(plan.agentBrowserVersion || '').trim()
  if (!version || version === 'latest') return ''
  return `https://github.com/${plan.agentBrowser.repo}/releases/download/${agentBrowserReleaseTag(version)}/${plan.agentBrowser.fileName}`
}

function runtimeBundleArchivePath(plan) {
  return join(repoRoot, 'dist', `${plan.bundleName}.zip`)
}

function outputRuntimeManifestPath(plan) {
  return join(plan.outputDir, 'harnessclaw-runtime-manifest.json')
}

function removeManagedRuntimeFiles(outputDir, keepPaths = []) {
  const keep = new Set(keepPaths.map((item) => resolve(item)))
  mkdirSync(outputDir, { recursive: true })
  for (const entry of readdirSync(outputDir)) {
    if (entry === 'README.md') continue
    const entryPath = join(outputDir, entry)
    if (keep.has(resolve(entryPath))) continue
    if (
      entry === 'harnessclaw-engine' ||
      entry === 'harnessclaw-engine.exe' ||
      entry.startsWith('harnessclaw-engine-') ||
      entry === 'agent-browser' ||
      entry.startsWith('agent-browser-')
    ) {
      rmSync(entryPath, { recursive: true, force: true })
    }
  }
}

function runtimeManifestForPlan(plan, source) {
  return {
    schema_version: 1,
    platform: plan.platform,
    arch: plan.arch,
    binaries: {
      agent_browser: {
        path: plan.agentBrowser.fileName,
        version: plan.agentBrowserVersion,
        repo: plan.agentBrowser.repo,
        source,
      },
    },
  }
}

function writeOutputRuntimeManifest(plan, source) {
  writeFileSync(outputRuntimeManifestPath(plan), `${JSON.stringify(runtimeManifestForPlan(plan, source), null, 2)}\n`, 'utf8')
}

function preparedAgentBrowserMatches(plan) {
  if (!existsSync(plan.agentBrowser.targetPath)) return false
  const manifestPath = outputRuntimeManifestPath(plan)
  if (!existsSync(manifestPath)) return false
  let manifest
  try {
    manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
  } catch {
    return false
  }
  const agentBrowser = manifest && manifest.binaries && manifest.binaries.agent_browser
  return manifest.platform === plan.platform
    && manifest.arch === plan.arch
    && agentBrowser
    && agentBrowser.path === plan.agentBrowser.fileName
    && agentBrowser.version === plan.agentBrowserVersion
    && agentBrowser.repo === plan.agentBrowser.repo
}

function resolveGoBinary(env = process.env) {
  if (env.GO) return env.GO
  const homebrewGo = '/opt/homebrew/bin/go'
  if (existsSync(homebrewGo)) return homebrewGo
  return 'go'
}

function buildEngine(plan, env = process.env) {
  const result = spawnSync(resolveGoBinary(env), ['build', '-trimpath', '-ldflags=-s -w', '-o', plan.engine.targetPath, './cmd/server'], {
    cwd: repoRoot,
    stdio: 'inherit',
    env: {
      ...env,
      GOOS: plan.goos,
      GOARCH: plan.goarch,
      CGO_ENABLED: env.CGO_ENABLED || '0',
      GOTOOLCHAIN: env.GOTOOLCHAIN || 'auto',
    },
  })

  if (result.error) {
    throw result.error
  }
  if (result.status !== 0) {
    process.exit(result.status || 1)
  }
  if (plan.platform !== 'windows') {
    chmodSync(plan.engine.targetPath, 0o755)
  }
  process.stdout.write(`Built ${plan.engine.fileName} to ${plan.engine.targetPath}\n`)
}

function installAgentBrowserFromPath(sourcePath, plan, sourceLabel) {
  const tempPath = `${plan.agentBrowser.targetPath}.${process.pid}.tmp`
  rmSync(tempPath, { force: true })
  try {
    copyFileSync(sourcePath, tempPath)
    if (plan.platform !== 'windows') {
      chmodSync(tempPath, 0o755)
    }
    renameSync(tempPath, plan.agentBrowser.targetPath)
  } finally {
    rmSync(tempPath, { force: true })
  }
  writeOutputRuntimeManifest(plan, sourceLabel)
  process.stdout.write(`Copied ${plan.agentBrowser.fileName} from ${sourceLabel} to ${plan.agentBrowser.targetPath}\n`)
}

async function installAgentBrowserFromUrl(url, headers, plan, sourceLabel) {
  const tempPath = `${plan.agentBrowser.targetPath}.${process.pid}.download`
  rmSync(tempPath, { force: true })
  try {
    await downloadToFile(url, headers, tempPath)
    if (plan.platform !== 'windows') {
      chmodSync(tempPath, 0o755)
    }
    renameSync(tempPath, plan.agentBrowser.targetPath)
  } finally {
    rmSync(tempPath, { force: true })
  }
  writeOutputRuntimeManifest(plan, sourceLabel)
  process.stdout.write(`Downloaded ${plan.agentBrowser.fileName} from ${sourceLabel} to ${plan.agentBrowser.targetPath}\n`)
}

function copyAgentBrowserFromRuntimeBundle(plan) {
  const archivePath = runtimeBundleArchivePath(plan)
  if (!existsSync(archivePath)) return false

  const manifestResult = spawnSync('unzip', ['-p', archivePath, 'manifest.json'], {
    encoding: 'utf8',
    maxBuffer: 1024 * 1024,
  })
  if (manifestResult.status !== 0 || manifestResult.error) return false

  let manifest
  try {
    manifest = JSON.parse(manifestResult.stdout)
  } catch {
    return false
  }

  const binaryPath = manifest && manifest.binaries && manifest.binaries.agent_browser && manifest.binaries.agent_browser.path
  const binaryVersion = manifest && manifest.binaries && manifest.binaries.agent_browser && manifest.binaries.agent_browser.version
  if (manifest.platform !== plan.platform || manifest.arch !== plan.arch) return false
  if (binaryVersion !== plan.agentBrowserVersion) return false
  if (binaryPath !== `bin/${plan.agentBrowser.fileName}`) return false

  const extractResult = spawnSync('unzip', ['-p', archivePath, binaryPath], {
    encoding: null,
    maxBuffer: 64 * 1024 * 1024,
  })
  if (extractResult.error) {
    throw extractResult.error
  }
  if (extractResult.status !== 0) return false

  const tempPath = `${plan.agentBrowser.targetPath}.${process.pid}.bundle`
  rmSync(tempPath, { force: true })
  try {
    writeFileSync(tempPath, extractResult.stdout)
    if (plan.platform !== 'windows') {
      chmodSync(tempPath, 0o755)
    }
    renameSync(tempPath, plan.agentBrowser.targetPath)
  } catch (error) {
    process.stderr.write(`Warning: ignored ${archivePath}: ${error.message}\n`)
    return false
  } finally {
    rmSync(tempPath, { force: true })
  }
  writeOutputRuntimeManifest(plan, archivePath)
  process.stdout.write(`Restored ${plan.agentBrowser.fileName} from ${archivePath} to ${plan.agentBrowser.targetPath}\n`)
  return true
}

async function downloadAgentBrowser(plan, env = process.env) {
  const localBinary = (env.AGENT_BROWSER_NATIVE_BINARY || '').trim()
  if (localBinary) {
    const sourcePath = resolve(localBinary)
    if (!existsSync(sourcePath)) {
      throw new Error(`AGENT_BROWSER_NATIVE_BINARY does not exist: ${sourcePath}`)
    }
    installAgentBrowserFromPath(sourcePath, plan, sourcePath)
    return
  }

  if (preparedAgentBrowserMatches(plan)) {
    if (plan.platform !== 'windows') {
      chmodSync(plan.agentBrowser.targetPath, 0o755)
    }
    process.stdout.write(`Reusing prepared ${plan.agentBrowser.fileName} at ${plan.agentBrowser.targetPath}\n`)
    return
  }

  const token = env.AGENT_BROWSER_GITHUB_TOKEN || env.GH_TOKEN || env.GITHUB_TOKEN
  const headers = {
    Accept: 'application/vnd.github+json',
    'User-Agent': 'harnessclaw-engine-runtime-fetcher',
  }
  if (token) {
    headers.Authorization = `Bearer ${token}`
  }

  const directUrl = directAgentBrowserAssetUrl(plan)
  try {
    if (directUrl) {
      await installAgentBrowserFromUrl(directUrl, headers, plan, `agent-browser ${agentBrowserReleaseTag(plan.agentBrowserVersion)}`)
      return
    }

    const releaseApiUrl = `https://api.github.com/repos/${plan.agentBrowser.repo}/releases/latest`
    const release = await fetchJson(releaseApiUrl, headers)
    const asset = Array.isArray(release.assets)
      ? release.assets.find((item) => item && item.name === plan.agentBrowser.fileName)
      : null
    if (!asset || !asset.browser_download_url) {
      throw new Error(`Asset ${plan.agentBrowser.fileName} not found in agent-browser release ${release.tag_name || '<unknown>'}`)
    }

    await installAgentBrowserFromUrl(asset.browser_download_url, headers, plan, `agent-browser ${release.tag_name}`)
  } catch (error) {
    if (copyAgentBrowserFromRuntimeBundle(plan)) return
    throw error
  }
}

function writeManifest(plan) {
  if (!plan.manifestPath) return
  mkdirSync(dirname(plan.manifestPath), { recursive: true })
  const manifest = {
    schema_version: 1,
    name: plan.bundleName,
    platform: plan.platform,
    arch: plan.arch,
    created_at: new Date().toISOString(),
    engine: plan.includeEngine
      ? {
          path: `bin/${plan.engine.fileName}`,
          goos: plan.goos,
          goarch: plan.goarch,
        }
      : null,
    binaries: {
      agent_browser: {
        path: `bin/${plan.agentBrowser.fileName}`,
        version: plan.agentBrowserVersion,
        repo: plan.agentBrowser.repo,
      },
    },
  }
  writeFileSync(plan.manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, 'utf8')
  process.stdout.write(`Wrote runtime manifest to ${plan.manifestPath}\n`)
}

function createArchive(plan) {
  if (!plan.archivePath) return
  mkdirSync(dirname(plan.archivePath), { recursive: true })
  rmSync(plan.archivePath, { force: true })
  const result = spawnSync('zip', ['-qry', plan.archivePath, '.'], {
    cwd: plan.bundleRoot,
    stdio: 'inherit',
  })
  if (result.error) {
    throw result.error
  }
  if (result.status !== 0) {
    process.exit(result.status || 1)
  }
  process.stdout.write(`Created runtime bundle ${plan.archivePath}\n`)
}

async function prepareRuntime(plan = createRuntimePlan(), env = process.env) {
  if (plan.archivePath) {
    rmSync(plan.bundleRoot, { recursive: true, force: true })
  }
  mkdirSync(plan.outputDir, { recursive: true })
  removeManagedRuntimeFiles(plan.outputDir, [plan.agentBrowser.targetPath])
  if (plan.includeEngine) {
    buildEngine(plan, env)
  }
  await downloadAgentBrowser(plan, env)
  writeManifest(plan)
  createArchive(plan)
  return plan
}

async function main() {
  const plan = createRuntimePlan()
  if (parseArgs(process.argv.slice(2)).printAgentBrowserPath) {
    process.stdout.write(`${plan.agentBrowser.targetPath}\n`)
    return
  }
  await prepareRuntime(plan)
}

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${String(error)}\n`)
    process.exit(1)
  })
}

module.exports = {
  agentBrowserFileName,
  createRuntimePlan,
  directAgentBrowserAssetUrl,
  engineFileName,
  normalizeRuntimeArch,
  normalizeRuntimePlatform,
  parseArgs,
  prepareRuntime,
}
