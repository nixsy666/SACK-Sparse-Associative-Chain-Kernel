// ============================================================================
// CONSTANTS
// ============================================================================
const NEURON_COUNT = 50;
const FIELD_SIZE = 1024;
const COARSE_CELLS = 16;
const PIPE_GAP = 12;
const PIPE_SPACING = 40;
const WORLD_HEIGHT = 40;
const GRAVITY = 0.1;
const FLAP_FORCE = -0.8;
const MAX_VEL = 1.1;
const DENT_WEIGHT = 0.32;

// ============================
// random generator between a-b
// ============================
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
// ============================================================================
function Neuron(nid) {
  this.nid = nid;
  this.field = new Float32Array(FIELD_SIZE);
  this.cell = Math.floor(Math.random() * COARSE_CELLS);
  this.activations = 0;
  this.successes = 0;
  this.failures = 0;
  // Seed random resonance peak
  var peak = Math.floor(Math.random() * FIELD_SIZE);
  this.field[peak] = 1.0;
  for (var i = 1; i <= 8; i++) {
    var w = 1.0 - (i / 9);
    this.field[(peak + i) % FIELD_SIZE] = w;
    this.field[(peak - i + FIELD_SIZE) % FIELD_SIZE] = w;
  }
  this.peakPos = peak;
}

/* Neuron.prototype.resonate = function(fieldPos, speed) {
  var raw = this.field[fieldPos];
  var total = this.successes + this.failures + 0.001;
  var confidence = this.successes / total;
  return raw * confidence * (1 + speed * 0.2);
}; */

Neuron.prototype.resonate = function(fieldPos, speed) {
  var raw = this.field[fieldPos];
  var total = this.successes + this.failures + 0.001;
  var confidence = this.successes / total;
  var floor = 0.1; // exploration minimum
  return raw * Math.max(floor, confidence) * (1 + speed * 0.2);
};

Neuron.prototype.dent = function(fieldPos) {
  // Crash - crater the field around this position
  for (var i = 0; i < 16; i++) {
    var w = 1.0 - (i / 16);
    var p1 = (fieldPos + i) % FIELD_SIZE;
    var p2 = (fieldPos - i + FIELD_SIZE) % FIELD_SIZE;
    this.field[p1] = Math.max(-1.0, this.field[p1] - DENT_WEIGHT * w);
    this.field[p2] = Math.max(-1.0, this.field[p2] - DENT_WEIGHT * w);
  }
};

Neuron.prototype.dull = function(fieldPos) {
  // Low confidence - soften neighbours
  for (var i = 0; i < 8; i++) {
    var w = 1.0 - (i / 8);
    var p1 = (fieldPos + i) % FIELD_SIZE;
    var p2 = (fieldPos - i + FIELD_SIZE) % FIELD_SIZE;
    this.field[p1] *= (1 - 0.1 * w);
    this.field[p2] *= (1 - 0.1 * w);
  }
};

Neuron.prototype.bounce = function(fieldPos, pathConf) {
  // Success - elevate field around this position
  var lift = 0.05 * Math.min(2.0, pathConf);
  for (var i = 0; i < 12; i++) {
    var w = 1.0 - (i / 12);
    var p1 = (fieldPos + i) % FIELD_SIZE;
    var p2 = (fieldPos - i + FIELD_SIZE) % FIELD_SIZE;
    this.field[p1] = Math.min(1.0, this.field[p1] + lift * w);
    this.field[p2] = Math.min(1.0, this.field[p2] + lift * w);
  }
  // Migrate cell registration toward this signal
  var newCell = coarseCell(fieldPos / FIELD_SIZE * 180);
  if (Math.random() < 0.05) this.cell = newCell;
  this.activations++;
  this.successes++;
};

// ============================================================================
// SACK
// ============================================================================
function SACK() {
  this.neurons = [];
  for (var i = 0; i < NEURON_COUNT; i++) {
    this.neurons.push(new Neuron(i));
  }
  // Index by coarse cell
  this.cellIndex = {};
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);
  this.lastChain = [];
  this.lastConf = 0;
  this.runs = 0;
}

SACK.prototype.rebuildIndex = function() {
  for (var c = 0; c < COARSE_CELLS; c++) this.cellIndex[c] = [];
  this.neurons.forEach(function(n) {
    this.cellIndex[n.cell].push(n.nid);
  }, this);
};

SACK.prototype.decide = function(angleDeg, distance, speed, pathConf) {
  var fieldPos = angleToField(angleDeg, distance, speed);
  var cell = coarseCell(angleDeg);

  // Grab candidates from this cell and neighbours
  var candidates = [];
  for (var c = cell - 1; c <= cell + 1; c++) {
    var cc = (c + COARSE_CELLS) % COARSE_CELLS;
    var nids = this.cellIndex[cc] || [];
    for (var i = 0; i < nids.length; i++) {
      candidates.push(nids[i]);
    }
  }

  // If sparse, open to all
  if (candidates.length < 4) {
    for (var n = 0; n < NEURON_COUNT; n++) candidates.push(n);
  }

  // Deduplicate
  var seen = {};
  var unique = [];
  for (var j = 0; j < candidates.length; j++) {
    if (!seen[candidates[j]]) { seen[candidates[j]] = true; unique.push(candidates[j]); }
  }

  // Propagate signal - instant sweep, find highest resonance
  var best = -Infinity;
  var bestNid = unique[0];
  for (var k = 0; k < unique.length; k++) {
    var r = this.neurons[unique[k]].resonate(fieldPos, speed);
    if (r > best) { best = r; bestNid = unique[k]; }
  }

  // Random tiebreak among equals
  var tied = unique.filter(function(nid) {
    return Math.abs(this.neurons[nid].resonate(fieldPos, speed) - best) < 0.01;
  }, this);
  bestNid = tied[Math.floor(Math.random() * tied.length)];

  this.lastConf = best;
  this.lastChain.push({ nid: bestNid, fieldPos: fieldPos, pathConf: pathConf });

  // Flap if resonance positive
  return { flap: this.field_response(bestNid, fieldPos) > 0, nid: bestNid };
};

SACK.prototype.field_response = function(nid, fieldPos) {
  return this.neurons[nid].field[fieldPos];
};

SACK.prototype.reinforceChain = function(reward, fieldPos, pathConf) {
  var chain = this.lastChain;
  for (var i = 0; i < chain.length; i++) {
    var n = this.neurons[chain[i].nid];
    if (reward > 0) {
      n.bounce(chain[i].fieldPos, pathConf);
    } else {
      n.failures++;
    }
  }
};

SACK.prototype.applyCrash = function(fieldPos) {
  var chain = this.lastChain;
  var len = chain.length;
  if (len === 0) return;

  // Crash neuron - full dent
  this.neurons[chain[len - 1].nid].dent(chain[len - 1].fieldPos);
  this.neurons[chain[len - 1].nid].failures++;

  // One before - fail + dull
  if (len >= 2) {
    this.neurons[chain[len - 2].nid].dull(chain[len - 2].fieldPos);
    this.neurons[chain[len - 2].nid].failures++;
  }

  // Two before - light dull
  if (len >= 3) {
    this.neurons[chain[len - 3].nid].dull(chain[len - 3].fieldPos);
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
  var chainLen = this.lastChain.length;
  return {
    neurons: NEURON_COUNT,
    active: active,
    dead: dead,
    chainLen: chainLen,
    successes: totalSucc,
    failures: totalFail,
    conf: this.lastConf
  };
};

// ============================================================================
// FLAPPY WORLD
// ============================================================================
function FlappyWorld() {
  this.birdY = WORLD_HEIGHT / 2;
  this.birdVel = 0;
  this.pipes = [];
  this.score = 0;
  this.tick = 0;
  this._spawnPipe();
}

FlappyWorld.prototype._spawnPipe = function() {
  var gapTop = Math.floor(Math.random() * (WORLD_HEIGHT - PIPE_GAP - 4)) + 2;
  this.pipes.push({ x: PIPE_SPACING, gapTop: gapTop });
};

FlappyWorld.prototype.getSignal = function() {
  if (this.pipes.length === 0) return { angle: 90, distance: 1, speed: 0 };
  var pipe = this.pipes[0];
  var gapMid = pipe.gapTop + PIPE_GAP / 2;
  var dy = gapMid - this.birdY;
  var dx = Math.max(pipe.x, 1);
  var angleRad = Math.atan2(dy, dx);
  var angleDeg = (angleRad * 180 / Math.PI + 180) % 180;
  var distance = Math.min(1.0, Math.max(0.0, pipe.x / PIPE_SPACING));
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
function printStats(sack, run, score, pipesPassed, mode, allScores) {
  var s = sack.stats();
  var recent = allScores.slice(-8).join(" ");
  process.stdout.write("\x1B[2J\x1B[0f");
  console.log("====================================================");
  console.log("  SACK FLAPPY v4  |  Run " + run + "  |  Score " + score + "  |  " + mode);
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
    var world = new FlappyWorld();
    var alive = true;
    var pipesPassed = 0;
    var pathConf = 1.0;
    var useRandom = runNumber <= 2;
    var mode = useRandom ? "RANDOM" : "SACK";
    sack.resetChain();

    function tick() {
      if (!alive) {
        allScores.push(world.score);
        printStats(sack, runNumber, world.score, pipesPassed, mode, allScores);
        console.log("  Run ended after " + world.tick + " ticks");
        setTimeout(doRun, 2800);
        return;
      }

      var sig = world.getSignal();
      var flap, result;

/*       if (useRandom) {
        flap = world.birdY > (WORLD_HEIGHT / 2);
      } */ 
	  
	  if (useRandom) {
		var sig = world.getSignal();
		// Simple geometry: if bird is below the gap centre, flap
		var gapMid = world.pipes[0] ? world.pipes[0].gapTop + PIPE_GAP / 2 : WORLD_HEIGHT / 2;
		flap = this.birdY > gapMid;
      } else {
        result = sack.decide(sig.angle, sig.distance, sig.speed, pathConf);
        flap = result.flap;
      }

      var step = world.step(flap);
      alive = step.alive;
      var passed = step.passed;

      if (!useRandom) {
        if (passed) {
          pipesPassed++;
          sack.reinforceChain(1.0, angleToField(sig.angle, sig.distance, sig.speed), pathConf);
          pathConf = Math.min(2.0, pathConf * 1.05);
          sack.resetChain();
        }
        if (!alive) {
          var fp = angleToField(sig.angle, sig.distance, sig.speed);
          sack.reinforceChain(-1.0, fp, pathConf);
          sack.applyCrash(fp);
          sack.rebuildIndex();
        }
      }

      if (world.tick % 20 === 0) {
        printStats(sack, runNumber, world.score, pipesPassed, mode, allScores);
      }

      setImmediate(tick);
    }

    tick();
  }

  doRun();
}

runGame();