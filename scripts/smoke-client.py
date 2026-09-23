"""The shipped Python client against a live box.

Driven by scripts/smoke-api.sh, which has already started the server and put a
key in the environment. What it covers is the half the shell script cannot: the
client's own contract — that a query which outlives its window comes back as a
job, and that following that job to its answer works.

Prints one line per check; a failure is an exception, which the shell reports.
"""

import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "clients", "python"))

from claudebox import ClaudeBox, timestamped  # noqa: E402

NAME = "smoke-client"


def main() -> None:
    box = ClaudeBox(os.environ["CBX_URL"], os.environ["CBX_KEY"])

    box.create(NAME, system_prompt="Answer in as few words as possible.")
    assert any(s.name == NAME for s in box.sessions()), "the new session is not listed"
    print("the client creates a session and lists it")

    artifact = timestamped("client.html")
    answer = box.query(
        NAME,
        f"Write {artifact} containing exactly <h1>ok</h1>, then reply DONE.",
        "5m",
        artifacts=[artifact],
    )
    assert not answer.pending, f"expected an answer within 5m, got {answer.status}"
    print("a query inside its window answers directly")

    paths = [a.path for a in box.artifacts(NAME)]
    assert artifact in paths, f"the declared artifact is missing: {paths}"
    assert b"<h1>ok</h1>" in box.fetch(NAME, artifact), "the artifact came back wrong"
    print("a declared artifact is listed and fetchable")

    # respond_within 0 is the job path: the box answers 202 without waiting.
    started = box.query(NAME, "Reply with exactly: LATER", 0)
    assert started.pending, f"expected a job, got {started.status}"
    assert started.job, "a pending answer with no job id is unfollowable"
    print("a query that cannot finish in time returns a job")

    deadline = time.monotonic() + 300
    job = box.job(started.job, respond_within="30s")
    while job.pending and time.monotonic() < deadline:
        job = box.job(started.job, respond_within="30s")
    assert job.status == "done", f"the job did not finish: {job.status} {job.error}"
    assert "LATER" in job.answer.upper(), f"the job's answer is wrong: {job.answer!r}"
    print("following the job to its end returns the answer")

    box.delete(NAME)
    assert not any(s.name == NAME for s in box.sessions()), "the session survived delete"
    print("the client deletes the session")


main()
