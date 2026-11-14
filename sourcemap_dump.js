// Dump sources from a sourcemap
const fs = require('fs');
const path = require('path');

const mapFile = process.argv[2];
const outDir = process.argv[3];
const map = JSON.parse(fs.readFileSync(mapFile, 'utf8'));

if (!map.sources) { console.error('no sources'); process.exit(1); }
map.sources.forEach((src, i) => {
  const content = (map.sourcesContent && map.sourcesContent[i]) || '';
  // webpack:///./src/... -> strip prefix
  let rel = src.replace(/^webpack:\/\/\//, '').replace(/^\.\//, '');
  rel = rel.replace(/[<>:"|?*]/g, '_');
  const dest = path.join(outDir, rel);
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  fs.writeFileSync(dest, content);
  console.log(rel + '\t' + content.length);
});
console.log('total: ' + map.sources.length);
