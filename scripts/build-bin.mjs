import { build } from 'esbuild'
import { copyFile, chmod, mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawn } from 'node:child_process'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const projectRoot = resolve(__dirname, '..')
const args = process.argv.slice(2)
const getArg = name => {
  const index = args.indexOf(name)
  return index >= 0 ? args[index + 1] : undefined
}
const packagePath = join(projectRoot, 'package.json')
const packageName = JSON.parse(await readFile(packagePath, 'utf8')).name || 'app'
const distDir = resolve(process.env.BUILD_DIR || getArg('--out-dir') || join(projectRoot, 'dist'))
const binBaseName = process.env.BIN_NAME || getArg('--bin-name') || packageName
const isWindows = process.platform === 'win32'
const binName = isWindows ? `${binBaseName.replace(/\.exe$/, '')}.exe` : binBaseName
const seaEntry = join(distDir, 'sea-entry.cjs')
const seaConfig = join(distDir, 'sea-config.json')
const seaBlob = join(distDir, 'sea-prep.blob')
const binPath = join(distDir, binName)
const sourcePath = join(projectRoot, 'llm-proxy-lite.js')

function run(command, args) {
  return new Promise((resolvePromise, reject) => {
    const child = spawn(command, args, {
      cwd: projectRoot,
      stdio: 'inherit',
      env: process.env,
      shell: false,
    })
    child.on('error', reject)
    child.on('close', code => {
      if (code === 0) return resolvePromise()
      reject(new Error(`${command} ${args.join(' ')} exited with code ${code}`))
    })
  })
}

async function main() {
  await mkdir(distDir, { recursive: true })

  await build({
    entryPoints: [sourcePath],
    outfile: seaEntry,
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    sourcemap: false,
    logLevel: 'info',
  })

  const config = {
    main: seaEntry,
    output: seaBlob,
    disableExperimentalSEAWarning: true,
  }
  await writeFile(seaConfig, JSON.stringify(config, null, 2))

  await run(process.execPath, ['--experimental-sea-config', seaConfig])
  await copyFile(process.execPath, binPath)

  const postjectBin = resolve(projectRoot, 'node_modules', '.bin', isWindows ? 'postject.cmd' : 'postject')
  const postjectArgs = [
    binPath,
    'NODE_SEA_BLOB',
    seaBlob,
    '--sentinel-fuse',
    'NODE_SEA_FUSE_fce680ab2cc467b6e072b8b5df1996b2',
  ]

  if (process.platform === 'darwin') {
    postjectArgs.push('--macho-segment-name', 'NODE_SEA')
  }

  await run(postjectBin, postjectArgs)

  if (process.platform === 'darwin') {
    console.log('Signing binary for macOS...')
    await run('codesign', ['--force', '--sign', '-', binPath])
  }

  if (!isWindows) {
    await chmod(binPath, 0o755)
  }

  console.log(`\n✅ Binary created: ${binPath}`)
}

main().catch(err => {
  console.error('❌ build:bin failed:', err.message)
  process.exit(1)
})
