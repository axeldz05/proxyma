import sys
import json
import os
import glob
from datetime import datetime

# Auto re-exec using parent services-examples .venv/bin/python if invoked with system python
script_dir = os.path.dirname(os.path.abspath(__file__))
services_root = os.path.abspath(os.path.join(script_dir, ".."))
venv_python = os.path.join(services_root, ".venv", "bin", "python")
if os.path.exists(venv_python) and sys.executable != venv_python and "PROXYMA_REEXEC" not in os.environ:
    os.environ["PROXYMA_REEXEC"] = "1"
    os.execv(venv_python, [venv_python] + sys.argv)

for path in glob.glob(os.path.join(services_root, ".venv", "lib", "python*", "site-packages")) + glob.glob(os.path.join("/tmp", ".local", "lib", "python*", "site-packages")):
    if path not in sys.path:
        sys.path.insert(0, path)

def main():
    try:
        # Read payload from stdin
        payload = json.load(sys.stdin)
        text = payload.get("text", "")
        vault_path = payload.get("vault_path")
        note_name = payload.get("note_name")

        if not vault_path:
            vault_path = os.environ.get("OBSIDIAN_VAULT_PATH", "/home/drusila/Obsidian/MainVault")

        # Generate a timestamped note_name if not provided
        now = datetime.now()
        timestamp_str = now.strftime("%Y-%m-%d %H:%M:%S")
        if not note_name:
            file_timestamp = now.strftime("%Y%m%d_%H%M%S")
            note_name = f"ocr_note_{file_timestamp}.md"

        # Ensure note_name has .md extension
        if not note_name.endswith(".md"):
            note_name += ".md"

        # Ensure vault path directory exists
        os.makedirs(vault_path, exist_ok=True)
        note_path = os.path.join(vault_path, note_name)

        if not os.path.exists(note_path):
            # Create a new note
            with open(note_path, "w", encoding="utf-8") as f:
                f.write(text)
            print(json.dumps({
                "status": "created",
                "message": "Note created successfully",
                "note_path": note_path
            }))
        else:
            # Append to existing note with timestamp header
            append_content = f"\n\n---\n\n## OCR Append - {timestamp_str}\n\n{text}"
            with open(note_path, "a", encoding="utf-8") as f:
                f.write(append_content)
            print(json.dumps({
                "status": "appended",
                "message": "Note already existed; appended content instead of creating a new note.",
                "note_path": note_path
            }))
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    main()
