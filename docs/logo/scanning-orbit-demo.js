(() => {
  "use strict";

  const stage = document.querySelector("#orbit-stage");
  const beam = document.querySelector("#orbit-beam");
  const statusCopy = document.querySelector("#status-copy");
  const stateButtons = [...document.querySelectorAll("[data-preview-state]")];
  const playButton = document.querySelector("#play-sequence");
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  const stateCopy = {
    idle: "Monitoring · last refresh 2 min ago",
    refreshing: "Polling 3 sources…",
    success: "Up to date · just now",
    stale: "Attention · GitHub data is stale",
  };

  let currentState = "idle";
  let sequenceTimers = [];

  function beamAngle() {
    const transform = getComputedStyle(beam).transform;
    if (!transform || transform === "none") {
      return -90;
    }

    const matrix = new DOMMatrixReadOnly(transform);
    return Math.atan2(matrix.b, matrix.a) * (180 / Math.PI);
  }

  function freezeBeam() {
    const angle = beamAngle();
    beam.getAnimations().forEach((animation) => animation.cancel());
    beam.style.transform = `rotate(${angle}deg)`;
    return angle;
  }

  function startOrbit(duration) {
    const start = freezeBeam();
    if (reducedMotion.matches) {
      beam.style.transform = "rotate(-90deg)";
      return;
    }

    beam.animate(
      [
        { transform: `rotate(${start}deg)` },
        { transform: `rotate(${start + 360}deg)` },
      ],
      {
        duration,
        iterations: Infinity,
        easing: "linear",
      },
    );
  }

  function settleAtTop() {
    const start = freezeBeam();
    if (reducedMotion.matches) {
      beam.style.transform = "rotate(-90deg)";
      return;
    }

    const normalized = ((start % 360) + 360) % 360;
    let distance = (270 - normalized + 360) % 360;
    if (distance < 130) {
      distance += 360;
    }
    const finish = start + distance;

    const settle = beam.animate(
      [
        { transform: `rotate(${start}deg)` },
        { transform: `rotate(${finish}deg)` },
      ],
      {
        duration: 650,
        easing: "cubic-bezier(0.16, 1, 0.3, 1)",
        fill: "forwards",
      },
    );

    settle.addEventListener("finish", () => {
      beam.style.transform = `rotate(${finish}deg)`;
      settle.cancel();
    }, { once: true });
  }

  function clearSequence() {
    sequenceTimers.forEach(window.clearTimeout);
    sequenceTimers = [];
    playButton.disabled = false;
  }

  function setState(state, { keepSequence = false } = {}) {
    if (!(state in stateCopy)) {
      return;
    }
    if (!keepSequence) {
      clearSequence();
    }

    currentState = state;
    stage.dataset.state = state;
    statusCopy.textContent = stateCopy[state];

    stateButtons.forEach((button) => {
      button.setAttribute("aria-pressed", String(button.dataset.previewState === state));
    });

    if (state === "idle") {
      startOrbit(8000);
    } else if (state === "refreshing") {
      startOrbit(1050);
    } else if (state === "success") {
      settleAtTop();
    } else {
      freezeBeam();
    }
  }

  function playSequence() {
    clearSequence();
    playButton.disabled = true;
    setState("refreshing", { keepSequence: true });

    sequenceTimers.push(window.setTimeout(() => {
      setState("success", { keepSequence: true });
    }, 2200));

    sequenceTimers.push(window.setTimeout(() => {
      setState("idle", { keepSequence: true });
      clearSequence();
    }, 3900));
  }

  stateButtons.forEach((button) => {
    button.addEventListener("click", () => setState(button.dataset.previewState));
  });
  playButton.addEventListener("click", playSequence);
  reducedMotion.addEventListener("change", () => setState(currentState));

  setState("idle");
})();
