// ============================================================================
// CONSTANTS
// ============================================================================
const NEURON_COUNT = 50;
const FIELD_SIZE = 1024;
const COARSE_CELLS = 16;
const PIPE_GAP = 12;
const WORLD_HEIGHT = 40;
const GRAVITY = 0.1;
const FLAP_FORCE = -0.8;
const MAX_VEL = 1.1;
const INTERSECT_THRESHOLD = 0.05;
const CHAIN_MAX = 8;

// ============================================================================
// RANDOM HELPER
// ============================================================================
function getRandomInt(min, max) {
  min = Math.ceil(min);
  max = Math.floor(max);
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

// ============================================================================
// SIGNAL HELPERS
// ============================================================================
function angleToField(angleDeg, distance, speed) {
  var a = Math.max(0, Math.min(179.9, angleDeg));
  var aPos = (a / 180) * (FIELD_SIZE * 0.6);
  var dPos = (1 - Math.min(1, distance)) * (FIELD_SIZE * 0.3);
  var sPos = Math.min(1, speed) * (FIELD_SIZE * 0.1);
  return Math.floor(aPos + dPos + sPos) % FIELD_SIZE;
}

function coarseCell(angleDeg) {
  return Math.floor(Math.max(0, Math.min(179.9, angleDeg)) / 180 * COARSE_CELLS) % COARSE_CELLS;
}

// ============================================================================
// NEURON
// Holds a resonance field. Incoming signal passes through, exit signal is
// the interference of incoming with stored field at that position.
// If exit intersects original signal strongly enough, neuron joins the chain.
// ============================================================================
function Neuron(nid) {
  this.nid = nid;
  this.field = new Float32Array(FIELD_SIZE);
  this.cell = Math.floor(Math.random() * COARSE_CELLS);
  this.activations = 0;
  this.successes = 0;
  this.failures = 0;

  // Wide falloff so incoming signal can find the peak
  var peak = Math.floor(Math.random() * FIELD_SIZE);
  this.field[peak] = 1.0;
  for (var i = 1; i <= 80; i++) {
    var w = 1.0 - (i / 81);
    this.field[(peak + i) % FIELD_SIZE] = w;
    this.field[(peak - i + FIELD_SIZE) % FIELD_SIZE] = w;
  }
  this.peakPos = peak;
}

// Signal passes through - returns interference of incoming with stored field
Neuron.prototype.passThrough = function(fieldPos, incomingStrength) {
  var fieldVal = this.field[fieldPos];
  return fieldVal * incomingStrength;
};

// Strengthen intersection point
Neuron.prototype.strengthen = function(fieldPos, amount) {
  for (var i = 0; i < 12; i++) {
    var w = (1.0 - (i / 12)) * amount;
    this.field[(fieldPos + i) % FIELD_SIZE] = Math.min(1.0, this.field[(fieldPos + i) % FIELD_SIZE] + w);
    this.field[(fieldPos - i + FIELD_SIZE) % FIELD_SIZE] = Math.min(1.0, this.field[(fieldPos - i + FIELD_SIZE) % FIELD_SIZE] + w);
  }
  this.activations++;
  this.successes++;
};

// Weaken intersection point
Neuron.prototype.weaken = function(fieldPos, amount) {
  for (var i = 0; i < 16; i++) {
    var w = (1.0 - (i / 16)) * amount;
    this.field[(fieldPos + i) % FIELD_SIZE] = Math.max(-1.0, this.field[(fieldPos + i) % FIELD_SIZE] - w);
    this.field[(fieldPos - i + FIELD_SIZE) % FIELD_SIZE] = Math.max(-1.0, this.field[(fieldPos - i + FIELD_SIZE) % FIELD_SIZE] - w);
  }
  this.failures++;
};

// ============================================================================
// SACK
// Forward phase: signal echoes through neurons, intersections form chain
// Back phase:    learned signal fires back down chain, exit = decision
// ============================================================================
function SACK() {
  this.neurons = [];
  for (var i = 0; i < NEURON_COUNT; i++) {
    this.neurons.push(new Neuron(i));
  }
  this.cellIndex = {};
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);
  this.lastChain = [];
  this.lastConf = 0;
}

SACK.prototype.rebuildIndex = function() {
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);
};


SACK.prototype.assembleChain = function(fieldPos, cell, incomingStrength) {
  var chain = [];
  var usedNids = {};
  var currentSignal = incomingStrength;
  var currentPos = fieldPos;

  var candidates = [];
  for (var c = cell - 1; c <= cell + 1; c++) {
    var cc = (c + COARSE_CELLS) % COARSE_CELLS;
    var nids = this.cellIndex[cc] || [];
    for (var i = 0; i < nids.length; i++) candidates.push(nids[i]);
  }
  if (candidates.length < 4) {
    for (var n = 0; n < NEURON_COUNT; n++) candidates.push(n);
  }

  var seen = {};
  var unique = [];
  for (var j = 0; j < candidates.length; j++) {
    if (!seen[candidates[j]]) { seen[candidates[j]] = true; unique.push(candidates[j]); }
  }

  for (var s = unique.length - 1; s > 0; s--) {
    var swap = Math.floor(Math.random() * (s + 1));
    var tmp = unique[s]; unique[s] = unique[swap]; unique[swap] = tmp;
  }

  for (var k = 0; k < unique.length && chain.length < CHAIN_MAX; k++) {
    var nid = unique[k];
    if (usedNids[nid]) continue;
    var exitSignal = this.neurons[nid].passThrough(currentPos, currentSignal);
    if (Math.abs(exitSignal) >= INTERSECT_THRESHOLD) {
      chain.push({ nid: nid, fieldPos: currentPos, signal: exitSignal });
      usedNids[nid] = true;
      // Signal drifts forward along field with each link
      currentSignal = Math.max(-2.0, Math.min(2.0, exitSignal + incomingStrength));
      currentPos = (currentPos + Math.max(1, Math.floor(Math.abs(exitSignal) * 10))) % FIELD_SIZE;
    }
  }

  return chain;
};

// BACK PHASE - learned signal fires back down chain, returns decision value
SACK.prototype.fireDecision = function(chain) {
  if (chain.length === 0) return 0;
  var backSignal = 0;
  for (var i = chain.length - 1; i >= 0; i--) {
    backSignal += this.neurons[chain[i].nid].field[chain[i].fieldPos];
  }
  return backSignal / chain.length;
};

// Assemble chain then fire decision
SACK.prototype.decide = function(angleDeg, distance, speed) {
  var fieldPos = angleToField(angleDeg, distance, speed);
  var cell = coarseCell(angleDeg);
  var chain = this.assembleChain(fieldPos, cell, 1.0);

  if (chain.length === 0) {
    this.lastChain = [];
    this.lastConf = 0;
    return { flap: Math.random() > 0.5, chainLen: 0 };
  }

  var decision = this.fireDecision(chain);
  this.lastChain = chain;
  this.lastConf = decision;
  return { flap: decision > 0, chainLen: chain.length };
};

// Strengthen all chain intersection points
SACK.prototype.reinforceSuccess = function(amount) {
  for (var i = 0; i < this.lastChain.length; i++) {
    this.neurons[this.lastChain[i].nid].strengthen(this.lastChain[i].fieldPos, amount * 0.05);
  }
};

// Weaken chain - harder near crash end
SACK.prototype.reinforceCrash = function() {
  var len = this.lastChain.length;
  if (len === 0) return;
  for (var i = 0; i < len; i++) {
    var proximity = (i + 1) / len;
    this.neurons[this.lastChain[i].nid].weaken(this.lastChain[i].fieldPos, 0.3 * proximity);
  }
};

SACK.prototype.resetChain = function() {
  this.lastChain = [];
};

SACK.prototype.stats = function() {
  var dead = 0, active = 0, totalSucc = 0, totalFail = 0;
  this.neurons.forEach(function(n) {
    if (n.activations === 0) dead++;
    else active++;
    totalSucc += n.successes;
    totalFail += n.failures;
  });
  return {
    neurons: NEURON_COUNT,
    active: active,
    dead: dead,
    chainLen: this.lastChain.length,
    successes: totalSucc,
    failures: totalFail,
    conf: this.lastConf
  };
};

// ============================================================================
// FLAPPY WORLD
// ============================================================================
function FlappyWorld(pipeSpacing) {
  this.pipeSpacing = pipeSpacing || 40;
  this.birdY = WORLD_HEIGHT / 2;
  this.birdVel = 0;
  this.pipes = [];
  this.score = 0;
  this.tick = 0;
  this._spawnPipe();
}

FlappyWorld.prototype._spawnPipe = function() {
  var gapTop = Math.floor(Math.random() * (WORLD_HEIGHT - PIPE_GAP - 4)) + 2;
  this.pipes.push({ x: this.pipeSpacing, gapTop: gapTop });
};

FlappyWorld.prototype.getSignal = function() {
  if (this.pipes.length === 0) return { angle: 90, distance: 1, speed: 0 };
  var pipe = this.pipes[0];
  var gapMid = pipe.gapTop + PIPE_GAP / 2;
  var dy = gapMid - this.birdY;
  var dx = Math.max(pipe.x, 1);
  var angleRad = Math.atan2(dy, dx);
  var angleDeg = (angleRad * 180 / Math.PI + 180) % 180;
  var distance = Math.min(1.0, Math.max(0.0, pipe.x / this.pipeSpacing));
  var speed = Math.abs(this.birdVel) / MAX_VEL;
  return { angle: angleDeg, distance: distance, speed: speed };
};

FlappyWorld.prototype.step = function(flap) {
  if (flap) this.birdVel = FLAP_FORCE;
  this.birdVel += GRAVITY;
  this.birdVel = Math.max(-MAX_VEL, Math.min(MAX_VEL, this.birdVel));
  this.birdY += this.birdVel;
  this.tick++;

  for (var i = 0; i < this.pipes.length; i++) this.pipes[i].x--;

  var passed = false;
  var self = this;
  this.pipes = this.pipes.filter(function(p) {
    if (p.x < -2) { passed = true; self.score++; self._spawnPipe(); return false; }
    return true;
  });

  if (this.birdY <= 0 || this.birdY >= WORLD_HEIGHT) return { alive: false, passed: passed };

  var pipe = this.pipes[0];
  if (pipe && pipe.x === 0) {
    if (this.birdY < pipe.gapTop || this.birdY > pipe.gapTop + PIPE_GAP) {
      return { alive: false, passed: passed };
    }
  }

  return { alive: true, passed: passed };
};

// ============================================================================
// STATS
// ============================================================================
function printStats(sack, run, score, pipesPassed, pipeSpacing, allScores) {
  var s = sack.stats();
  var recent = allScores.slice(-8).join(" ");
  process.stdout.write("\x1B[2J\x1B[0f");
  console.log("====================================================");
  console.log("  SACK FLAPPY v6  |  Run " + run + "  |  Score " + score + "  |  Spacing " + pipeSpacing);
  console.log("====================================================");
  console.log("  Neurons         : " + s.neurons);
  console.log("  Active          : " + s.active);
  console.log("  Dead            : " + s.dead);
  console.log("  Chain length    : " + s.chainLen);
  console.log("  Total successes : " + s.successes);
  console.log("  Total failures  : " + s.failures);
  console.log("  Last conf       : " + s.conf.toFixed(4));
  console.log("  Pipes passed    : " + pipesPassed);
  console.log("  Recent scores   : " + recent);
  console.log("====================================================");
}

// ============================================================================
// MAIN
// ============================================================================
function runGame() {
  var sack = new SACK();
  var runNumber = 0;
  var allScores = [];

  function doRun() {
    runNumber++;
    var pipeSpacing = getRandomInt(35, 50);
    var world = new FlappyWorld(pipeSpacing);
    var alive = true;
    var pipesPassed = 0;
    var pathConf = 1.0;
    sack.resetChain();

    function tick() {
      if (!alive) {
        allScores.push(world.score);
        printStats(sack, runNumber, world.score, pipesPassed, pipeSpacing, allScores);
        console.log("  Run ended after " + world.tick + " ticks");
        setTimeout(doRun, 500);
        return;
      }

      var sig = world.getSignal();
      var result = sack.decide(sig.angle, sig.distance, sig.speed);
      var flap = result.flap;

      var step = world.step(flap);
      alive = step.alive;
      var passed = step.passed;

      // Survival tick - small continuous reward for staying alive with active chain
      if (alive && result.chainLen > 0) {
        sack.reinforceSuccess(0.1);
      }

      if (passed) {
        pipesPassed++;
        sack.reinforceSuccess(pathConf);
        pathConf = Math.min(2.0, pathConf * 1.05);
        sack.resetChain();
      }

      if (!alive) {
        sack.reinforceCrash();
        sack.rebuildIndex();
      }

      if (world.tick % 5 === 0) {
        printStats(sack, runNumber, world.score, pipesPassed, pipeSpacing, allScores);
      }

      setImmediate(tick);
    }

    tick();
  }

  doRun();
}

runGame();