"""Export checked-in joint deployment templates beside the two source repos.

Never overwrite an existing deploy directory or copy runtime secrets/results.
Run: python scripts/export-backend-stack.py --out ../deploy
"""
import argparse
from pathlib import Path
import shutil
import json

def export(destination: Path):
    repository=Path(__file__).resolve().parent.parent
    source=repository / "deployments" / "backend-stack"
    destination=destination.resolve()
    if destination.exists():
        raise ValueError("destination exists; use a new deployment directory")
    if destination.name != "deploy":
        raise ValueError("joint helper layout requires deployment directory named deploy")
    if destination.parent != repository.parent or not (destination.parent / "sql-sentinel" / "go.mod").is_file():
        raise ValueError("destination must be beside AgentMesh and sql-sentinel source repos")
    if repository.name != "AgentMesh":
        raise ValueError("joint Compose layout requires source directory named AgentMesh")
    # Copy only reviewed template paths, even if setup was accidentally run in
    # the template directory and created .env/secrets or private output files.
    names=json.loads((source / "TEMPLATE-FILES.json").read_text(encoding="utf-8"))["files"]
    selected=[]
    for name in names:
        relative=Path(name)
        file=(source / relative).resolve()
        if relative.is_absolute() or ".." in relative.parts or not file.is_relative_to(source.resolve()) or not file.is_file():
            raise ValueError("invalid deployment template manifest")
        selected.append((file,relative))
    destination.mkdir()
    for file,relative in selected:
        target=destination / relative
        target.parent.mkdir(parents=True,exist_ok=True)
        shutil.copy2(file,target)
    print("Deployment templates exported; no runtime credentials were included")

if __name__=="__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--out",type=Path,required=True)
    args=parser.parse_args()
    export(args.out)
