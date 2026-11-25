"""Puts a signed lotus.exe into the Windows release archive and rewrites SHA256SUMS.

    python3 scripts/replace-windows-exe.py dist signed/lotus.exe
"""
import glob, hashlib, os, sys, zipfile

dist, exe = sys.argv[1], sys.argv[2]
(zpath,) = glob.glob(os.path.join(dist, "lotus-*-windows-amd64.zip"))
tmp = zpath + ".new"
with zipfile.ZipFile(zpath) as src, zipfile.ZipFile(tmp, "w", zipfile.ZIP_DEFLATED) as dst:
    for item in src.infolist():
        data = open(exe, "rb").read() if item.filename.endswith("/lotus.exe") else src.read(item.filename)
        dst.writestr(item, data)
os.replace(tmp, zpath)
lines = []
for p in sorted(glob.glob(os.path.join(dist, "lotus-*"))):
    lines.append("%s *%s" % (hashlib.sha256(open(p, "rb").read()).hexdigest(), os.path.basename(p)))
open(os.path.join(dist, "SHA256SUMS"), "w").write("\n".join(lines) + "\n")
print("\n".join(lines))
