import sys
import os
import glob
import json

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

from pypdf import PdfReader
from PIL import Image
import pytesseract

def main():
    try:
        # Read payload from stdin
        payload = json.load(sys.stdin)
        input_path = payload.get("input_path")

        if not input_path:
            print(json.dumps({"error": "Missing input_path in payload"}))
            sys.exit(1)

        if not os.path.exists(input_path):
            print(json.dumps({"error": f"Input file not found: {input_path}"}))
            sys.exit(1)

        _, ext = os.path.splitext(input_path.lower())
        full_text = ""

        if ext == ".pdf":
            reader = PdfReader(input_path)
            text_parts = []
            for page in reader.pages:
                text = page.extract_text()
                if text:
                    text_parts.append(text)
            full_text = "\n".join(text_parts)
        elif ext in [".png", ".jpg", ".jpeg", ".tiff", ".bmp", ".gif"]:
            full_text = pytesseract.image_to_string(Image.open(input_path))
        else:
            # Fallback to PDF parsing if not standard image
            try:
                reader = PdfReader(input_path)
                text_parts = []
                for page in reader.pages:
                    text = page.extract_text()
                    if text:
                        text_parts.append(text)
                full_text = "\n".join(text_parts)
            except Exception:
                print(json.dumps({"error": f"Unsupported file type for text extraction: {ext}"}))
                sys.exit(1)

        print(json.dumps({
            "status": "success",
            "message": "Text extracted successfully",
            "text": full_text
        }))
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    main()
