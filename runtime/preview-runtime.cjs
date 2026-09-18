// Run the private viewer in a generated directory. Source edits are mirrored
// for hot reload; Next's config/type/build writes stay
// outside the user's source and cannot affect version history or the public app.
const { createRequire } = require("node:module");
const { createServer } = require("node:http");
const { spawn } = require("node:child_process");
const fs = require("node:fs/promises");
const path = require("node:path");
const requireApp = createRequire("/workspace/package.json");
const next = requireApp("next");

async function main() {
  const basePath = process.env.ATOMS_PREVIEW_BASE_PATH;
  if (!basePath?.startsWith("/__atoms_preview/")) throw new Error("Missing private preview base path");
  const published = process.env.ATOMS_PUBLISHED === "true";
  const port = published ? 3001 : 3000;
  const dir = "/tmp/atoms-preview-app";
  await fs.mkdir(dir, {recursive:true});
  const excluded = name => name.startsWith(".next") || name.startsWith("next.config.") || ["tsconfig.json", "next-env.d.ts", "tsconfig.tsbuildinfo", ".git", ".atoms", "node_modules"].includes(name);
  const seen = new Map();
  const syncDirectory = async (source,target) => {
    try { if (!(await fs.lstat(target)).isDirectory()) await fs.rm(target,{force:true}); } catch(error) {if(error.code!=="ENOENT") throw error;}
    await fs.mkdir(target,{recursive:true});
    const names = (await fs.readdir(source)).filter(name => !excluded(name));
    for (const name of names) {
      const input=path.join(source,name), output=path.join(target,name);
      const stat=await fs.lstat(input);
      if (stat.isDirectory()) { await syncDirectory(input,output); continue; }
      if (!stat.isFile()) continue;
      const signature=`${stat.ino}:${stat.size}:${stat.mtimeMs}`;
      if (seen.get(input) !== signature) {
        // Replace old links from earlier runtime layouts rather than writing
        // through them into the original workspace.
        try { const existing=await fs.lstat(output); if (!existing.isFile()) await fs.rm(output,{recursive:true,force:true}); } catch(error) { if(error.code!=="ENOENT") throw error; }
        await fs.copyFile(input,output); seen.set(input,signature);
      }
    }
    for (const name of await fs.readdir(target)) {
      if (!excluded(name) && !names.includes(name)) await fs.rm(path.join(target,name),{recursive:true,force:true});
    }
  };
  // Next's route watcher does not follow linked app directories. Mirror source
  // files only when changed, and keep dependencies shared without copying them.
  for (const name of await fs.readdir(dir)) if ((await fs.lstat(path.join(dir,name))).isSymbolicLink() && name!=="node_modules") await fs.unlink(path.join(dir,name));
  try { await fs.symlink("/workspace/node_modules",path.join(dir,"node_modules")); } catch(error) {if(error.code!=="EEXIST") throw error;}
  const sync = () => syncDirectory("/workspace",dir);
  await sync();
  await fs.writeFile(path.join(dir,"next.config.mjs"), `import {createRequire} from "node:module";
  const require = createRequire("/workspace/package.json");
  export default async phase => {
    const load = require('/workspace/node_modules/next/dist/server/config').default;
    const raw = await load(phase, '/workspace', {rawConfig:true});
    const config = typeof raw === 'function' ? await raw(phase, {defaultConfig:require('/workspace/node_modules/next/dist/server/config-shared').defaultConfig}) : await raw;
    return {...config, basePath:${JSON.stringify(basePath)}, assetPrefix:${JSON.stringify(basePath)}, distDir:'.next', compress:false, devIndicators:false,
      typescript:{...config.typescript, tsconfigPath:'tsconfig.json'}};
  };\n`);
  await fs.writeFile(path.join(dir,"tsconfig.json"), JSON.stringify({extends:"/workspace/tsconfig.json",
    include:["next-env.d.ts", "/workspace/**/*.ts", "/workspace/**/*.tsx", ".next/types/**/*.ts"],
    exclude:["/workspace/node_modules", "/workspace/.next", "/workspace/.next-dev", "/workspace/.next-preview"]}));
  const app = next({ dev:true, dir, hostname:"0.0.0.0", port });
  await app.prepare();
  const handler = app.getRequestHandler();
  const server = createServer((request,response) => {
    if (request.url === "/") { response.writeHead(307,{Location:basePath+"/"}); response.end(); return; }
    if (request.url !== basePath && !request.url.startsWith(basePath+"/") && !request.url.startsWith(basePath+"?")) request.url=basePath+request.url;
    handler(request,response);
  });
  // getRequestHandler installs Next's WebSocket handler on the HTTP server.
  await new Promise((resolve,reject) => { server.once("error",reject); server.listen(port,"0.0.0.0",resolve); });
  let syncing = false;
  const refresh = setInterval(async () => { if(syncing) return; syncing=true; try {await sync();} catch(error) {console.error("Preview source sync:",error);} finally {syncing=false;} },1000);
  console.log(`Private preview ready on ${port}`);
  let publicServer;
  if (published) {
    publicServer = spawn("pnpm",["dev","--hostname","0.0.0.0","--port","3000"],{cwd:"/workspace",stdio:"inherit"});
    publicServer.on("exit",code => process.exit(code || 1));
  }
  for (const signal of ["SIGINT","SIGTERM"]) process.on(signal,() => {
    clearInterval(refresh); publicServer?.kill(signal); server.close();
    app.close().finally(() => process.exit(0));
    setTimeout(() => process.exit(0),5000).unref();
  });
}
main().catch(error => { console.error(error); process.exit(1); });
