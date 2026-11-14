// Minimal asar extractor
const fs = require('fs');
const path = require('path');

const ASAR = 'C:\\Users\\30955\\Desktop\\TokensAPI\\glacc_out.asar';
const OUTDIR = 'C:\\Users\\30955\\Desktop\\TokensAPI\\glacc_extract';

const fd = fs.openSync(ASAR, 'r');
const head = Buffer.alloc(16);
fs.readSync(fd, head, 0, 16, 0);
const headerSize = head.readUInt32LE(4);
const jsonLen = head.readUInt32LE(12);
const jsonBuf = Buffer.alloc(jsonLen);
fs.readSync(fd, jsonBuf, 0, jsonLen, 16);
const header = JSON.parse(jsonBuf.toString('utf8'));
const dataOffset = 8 + headerSize;

function walk(node, dir, cb) {
  if (!node.files) return;
  for (const [name, child] of Object.entries(node.files)) {
    const p = dir ? dir + '/' + name : name;
    if (child.files) walk(child, p, cb);
    else cb(p, child);
  }
}

const mode = process.argv[2] || 'list';
const filter = process.argv[3] ? process.argv[3].toLowerCase() : null;

let count = 0;
walk(header, '', (p, entry) => {
  count++;
  if (mode === 'list') {
    if (filter && !p.toLowerCase().includes(filter)) return;
    console.log(`${entry.size || 0}\t${p}${entry.unpacked ? ' [unpacked]' : ''}`);
  } else if (mode === 'extract') {
    if (filter && !p.toLowerCase().includes(filter)) return;
    if (entry.unpacked) return;
    const buf = Buffer.alloc(entry.size);
    fs.readSync(fd, buf, 0, entry.size, dataOffset + parseInt(entry.offset, 10));
    const dest = path.join(OUTDIR, p);
    fs.mkdirSync(path.dirname(dest), { recursive: true });
    fs.writeFileSync(dest, buf);
  }
});
if (mode === 'extract') console.error('extracted done');
else console.error('total entries: ' + count);
