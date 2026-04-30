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
const INTERSECT_THRESHOLD = 0.05; // minimum interference to form a link
const CHAIN_MAX = 8;              // max chain depth for flappy (shallow decision)

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
// Each neuron holds a field of resonance values.
// When a signal passes through, it exits with a modified signature.
// If that exit intersects the original signal, this neuron joins the chain.
// ============================================================================
function Neuron(nid) {
  this.nid = nid;
  this.field = new Float32Array(FIELD_SIZE);
  this.cell = Math.floor(Math.random() * COARSE_CELLS);
  this.activations = 0;
  this.successes = 0;
  this.failures = 0;

  // Seed a random resonance peak with falloff
  var peak = Math.floor(Math.random() * FIELD_SIZE);
  this.field[peak] = 1.0;
  for (var i = 1; i <= 8; i++) {
    var w = 1.0 - (i / 9);
    this.field[(peak + i) % FIELD_SIZE] = w;
    this.field[(peak - i + FIELD_SIZE) % FIELD_SIZE] = w;
  }
  this.peakPos = peak;
}

// Pass incoming signal through this neuron's field
// Returns the exit signal: intersection of incoming with stored field
Neuron.prototype.passThrough = function(incomingPos, incomingStrength) {
  var fieldVal = this.field[incomingPos];
  // Interference: incoming * field at this position
  var exitSignal = fieldVal * incomingStrength;
  return exitSignal;
};

// Strengthen intersection point - good chain member
Neuron.prototype.strengthen = function(fieldPos, amount) {
  for (var i = 0; i < 12; i++) {
    var w = (1.0 - (i / 12)) * amount;
    var p1 = (fieldPos + i) % FIELD_SIZE;
    var p2 = (fieldPos - i + FIELD_SIZE) % FIELD_SIZE;
    this.field[p1] = Math.min(1.0, this.field[p1] + w);
    this.field[p2] = Math.min(1.0, this.field[p2] + w);
  }
  this.activations++;
  this.successes++;
};

// Weaken intersection point - bad chain member
Neuron.prototype.weaken = function(fieldPos, amount) {
  for (var i = 0; i < 16; i++) {
    var w = (1.0 - (i / 16)) * amount;
    var p1 = (fieldPos + i) % FIELD_SIZE;
    var p2 = (fieldPos - i + FIELD_SIZE) % FIELD_SIZE;
    this.field[p1] = Math.max(-1.0, this.field[p1] - w);
    this.field[p2] = Math.max(-1.0, this.field[p2] - w);
  }
  this.failures++;
};

// ============================================================================
// SACK
// Forward phase: incoming signal echoes through neurons, 
//                intersections form links, chain assembles
// Back phase:    learned signal fires back down chain,
//                exit state of back signal = decision
// ============================================================================
function SACK() {
  this.neurons = [];
  for (var i = 0; i < NEURON_COUNT; i++) {
    this.neurons.push(new Neuron(i));
  }

  // Index neurons by coarse cell for fast lookup
  this.cellIndex = {};
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);

  this.lastChain = []; // [{nid, fieldPos, signal}]
  this.lastConf = 0;
}

SACK.prototype.rebuildIndex = function() {
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);
};

// FORWARD PHASE
// Signal enters, passes through neurons, intersections form chain
SACK.prototype.assembleChain = function(fieldPos, cell, incomingStrength) {
  var chain = [];
  var signal = incomingStrength;
  var usedNids = {};

  // Get candidates from this cell and neighbours
  var candidates = [];
  for (var c = cell - 1; c <= cell + 1; c++) {
    var cc = (c + COARSE_CELLS) % COARSE_CELLS;
    var nids = this.cellIndex[cc] || [];
    for (var i = 0; i < nids.length; i++) {
      candidates.push(nids[i]);
    }
  }
  if (candidates.length < 4) {
    for (var n = 0; n < NEURON_COUNT; n++) candidates.push(n);
  }

  // Deduplicate
  var seen = {};
  var unique = [];
  for (var j = 0; j < candidates.length; j++) {
    if (!seen[candidates[j]]) { seen[candidates[j]] = true; unique.push(candidates[j]); }
  }

  // Shuffle candidates - no preference, let interference decide
  for (var s = unique.length - 1; s > 0; s--) {
    var swap = Math.floor(Math.random() * (s + 1));
    var tmp = unique[s]; unique[s] = unique[swap]; unique[swap] = tmp;
  }

  // Forward pass: each neuron that intersects joins the chain
  // carries combined signal (its exit + original) forward
  var currentSignal = signal;
  for (var k = 0; k < unique.length && chain.length < CHAIN_MAX; k++) {
    var nid = unique[k];
    if (usedNids[nid]) continue;
    var neuron = this.neurons[nid];
    var exitSignal = neuron.passThrough(fieldPos, currentSignal);

    // Intersection check: if exit signal is strong enough, neuron joins chain
    if (Math.abs(exitSignal) >= INTERSECT_THRESHOLD) {
      chain.push({ nid: nid, fieldPos: fieldPos, signal: exitSignal });
      usedNids[nid] = true;
      // Combined signal carries forward: exit + original
      currentSignal = exitSignal + signal;
      // Clamp to prevent runaway
      currentSignal = Math.max(-2.0, Math.min(2.0, currentSignal));
    }
  }

  return chain;
};

// BACK PHASE
// Learned signal fires back down assembled chain
// Returns the decision: positive = flap, negative = don't flap
SACK.prototype.fireDecision = function(chain, originalFieldPos) {
  if (chain.length === 0) return 0;

  // Back signal starts from chain end, travels to start
  // Each node contributes its stored field value at the intersection point
  var backSignal = 0;
  for (var i = chain.length - 1; i >= 0; i--) {
    var node = chain[i];
    var fieldVal = this.neurons[node.nid].field[node.fieldPos];
    backSignal += fieldVal;
  }

  // Normalise by chain length
  backSignal = backSignal / chain.length;
  this.lastConf = backSignal;
  return backSignal;
};

// Main decide: assemble chain then fire decision
SACK.prototype.decide = function(angleDeg, distance, speed) {
  var fieldPos = angleToField(angleDeg, distance, speed);
  var cell = coarseCell(angleDeg);

  // Forward phase
  var chain = this.assembleChain(fieldPos, cell, 1.0);

  // If no chain formed, fall back to random
  if (chain.length === 0) {
    this.lastChain = [];
    this.lastConf = 0;
    return { flap: Math.random() > 0.5, chainLen: 0 };
  }

  // Back phase - decision from learned field state
  var decision = this.fireDecision(chain, fieldPos);

  this.lastChain = chain;
  return { flap: decision > 0, chainLen: chain.length };
};

// Reinforce chain on good outcome - strengthen all intersection points
SACK.prototype.reinforceSuccess = function(amount) {
  var chain = this.lastChain;
  for (var i = 0; i < chain.length; i++) {
    this.neurons[chain[i].nid].strengthen(chain[i].fieldPos, amount * 0.05);
  }
};

// Punish chain on crash - weaken intersection points, harder near crash end
SACK.prototype.reinforceCrash = function() {
  var chain = this.lastChain;
  var len = chain.length;
  if (len === 0) return;

  // Crash end gets hardest hit, earlier nodes lighter
  for (var i = 0; i < len; i++) {
    var proximity = (i + 1) / len; // 0 = early in chain, 1 = crash end
    this.neurons[chain[i].nid].weaken(chain[i].fieldPos, 0.3 * proximity);
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