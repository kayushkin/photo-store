"""Find the faces on photo-store's thumbnails and post them to photo-store.

It measures and decides nothing: for each photo the model has not scanned,
it fetches the thumbnail, finds every face with InsightFace's buffalo_l
models (detection and a 512-number description of each face), and posts
what it found to POST /face-scans, including a scan that found no face.
Matching faces to people is photo-store's matcher's job, in Go.

    python -I find_faces.py --store-url http://127.0.0.1:8329 --worker 0 --workers 3

Workers split the photos by id number, so several can run at once without
scanning the same photo twice. The models download to ~/.insightface on the
first run. buffalo_l is licensed for non-commercial use only.
"""

import argparse
import base64
import json
import sys
import urllib.error
import urllib.request

import cv2
import numpy as np
from insightface.app import FaceAnalysis

MODEL = "insightface-buffalo_l"


def call(store_url, method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(store_url + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--store-url", required=True)
    parser.add_argument("--worker", type=int, default=0)
    parser.add_argument("--workers", type=int, default=1)
    parser.add_argument("--threads", type=int, default=2, help="CPU threads for the model")
    parser.add_argument("--limit", type=int, default=0, help="stop after this many photos; 0 = all")
    arguments = parser.parse_args()

    import onnxruntime
    options = onnxruntime.SessionOptions()
    options.intra_op_num_threads = arguments.threads
    analysis = FaceAnalysis(name="buffalo_l", providers=["CPUExecutionProvider"],
                            allowed_modules=["detection", "recognition"], session_options=options)
    analysis.prepare(ctx_id=-1, det_size=(640, 640))

    done = 0
    skipped = set()
    while True:
        status, body = call(arguments.store_url, "GET", f"/face-scans/pending?model={MODEL}&limit=1000")
        if status != 200:
            sys.exit(f"pending: {status} {body[:300]!r}")
        mine = [photo_id for photo_id in json.loads(body)
                if int(photo_id.split("_")[1]) % arguments.workers == arguments.worker and photo_id not in skipped]
        if not mine:
            break
        for photo_id in mine:
            status, thumbnail = call(arguments.store_url, "GET", f"/photos/{photo_id}/thumbnail")
            image = cv2.imdecode(np.frombuffer(thumbnail, np.uint8), cv2.IMREAD_COLOR) if status == 200 else None
            if image is None:
                print(f"{photo_id}: no readable thumbnail ({status}); skipped", file=sys.stderr, flush=True)
                skipped.add(photo_id)
                continue
            height, width = image.shape[:2]
            faces = []
            for face in analysis.get(image):
                left, top, right, bottom = (float(v) for v in face.bbox)
                faces.append({
                    "box": {"left": left / width, "top": top / height, "right": right / width, "bottom": bottom / height},
                    "detection_score": float(face.det_score),
                    "embedding_float32_base64": base64.b64encode(np.asarray(face.embedding, dtype="<f4").tobytes()).decode(),
                })
            status, answer = call(arguments.store_url, "POST", "/face-scans", {"photo_id": photo_id, "model": MODEL, "faces": faces})
            if status != 201:
                sys.exit(f"{photo_id}: POST /face-scans {status} {answer[:300]!r}")
            done += 1
            if done % 100 == 0:
                print(f"worker {arguments.worker}: {done} photos scanned", flush=True)
            if arguments.limit and done >= arguments.limit:
                return
    print(f"worker {arguments.worker}: done, {done} photos scanned, {len(skipped)} skipped", flush=True)


if __name__ == "__main__":
    main()
