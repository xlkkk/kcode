# 棋牌规则引擎参考实现（斗地主 + 麻将）

本 skill 提供棋牌游戏规则模块的参考实现与测试策略。使用原则：**参考算法结构，适配策划案差异**——不同游戏的牌型体系、番数、特殊规则各不相同，以下代码是经过验证的骨架模板，生成项目代码时必须按策划案逐条核对差异点。

---

## 一、通用设计原则

1. **牌值归一化**：内部用整数表示牌，比较与判定只依赖整数，显示时才转文案。建议 `3=3 ... 10=10, J=11, Q=12, K=13, A=14, 2=15, 小王=16, 大王=17`。
2. **统计驱动判定**：先做 `rankCounts`（牌值 → 张数）与 `suitCounts`（花色 → 张数）统计，绝大多数牌型判定可退化为对计数分布的检查，避免逐张扫描。
3. **判定与比较分离**：`Parse(hand []int) Hand` 返回带类型的牌型结构；`Compare(a, b Hand) int` 只做同类型大小比较；跨类型先比牌型等级。
4. **纯函数、无状态**：规则函数禁止持有游戏状态（底分、倍数等），方便全枚举单测与模拟器复用。

---

## 二、斗地主牌型体系参考实现

### 2.1 牌型定义

```go
package rules

// Rank 牌值:3~10 为本身,J=11,Q=12,K=13,A=14,2=15,小王=16,大王=17
type Rank int

// HandType 牌型等级,数值越大等级越高
type HandType int

const (
    TypeInvalid HandType = iota
    TypeSingle             // 单张
    TypePair               // 对子
    TypeTriple             // 三张
    TypeTripleOne          // 三带一
    TypeTriplePair         // 三带二
    TypeStraight           // 顺子(5 张起,A 可作 14,不可跨 2)
    TypePairStraight       // 连对(3 对起,不可跨 2)
    TypeAirplane           // 飞机不带
    TypeAirplaneSingle     // 飞机带单
    TypeAirplanePair       // 飞机带对
    TypeBomb               // 炸弹(4 张)
    TypeRocket             // 王炸(大小王各一)
)
```

### 2.2 判定核心：rankCounts 分布检查

```go
// rankCounts 统计牌值出现次数,返回 map[Rank]count 与去重后的牌值列表
func rankCounts(cards []Rank) map[Rank]int {
    m := make(map[Rank]int, len(cards))
    for _, c := range cards {
        m[c]++
    }
    return m
}

// byCount 从 rankCounts 提取指定张数的牌值(升序)
func byCount(m map[Rank]int, want int) []Rank {
    out := []Rank{}
    for r, c := range m {
        if c == want {
            out = append(out, r)
        }
    }
    sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
    return out
}

// Parse 判定一手牌的牌型;无法识别返回 TypeInvalid
func Parse(cards []Rank) (HandType, Rank) {
    n := len(cards)
    if n == 0 {
        return TypeInvalid, 0
    }
    m := rankCounts(cards)
    maxCount := 0
    for _, c := range m {
        if c > maxCount {
            maxCount = c
        }
    }

    // 王炸:大小王各一张
    if n == 2 && m[16] == 1 && m[17] == 1 {
        return TypeRocket, 17
    }
    if maxCount == 4 && n == 4 {
        return TypeBomb, onlyRank(m)
    }
    if maxCount == 1 && n == 1 {
        return TypeSingle, cards[0]
    }
    if maxCount == 2 && n == 2 {
        return TypePair, cards[0]
    }
    if maxCount == 3 && n == 3 {
        return TypeTriple, cards[0]
    }
    if maxCount == 3 && n == 4 {
        return TypeTripleOne, byCount(m, 3)[0]
    }
    if maxCount == 3 && n == 5 && len(byCount(m, 2)) == 1 {
        return TypeTriplePair, byCount(m, 3)[0]
    }

    // 顺子:5 张起、连续、不跨 2、不含王
    if maxCount == 1 && n >= 5 {
        if isConsecutive(byCount(m, 1)) {
            return TypeStraight, byCount(m, 1)[0]
        }
    }
    // 连对:3 对起、连续、不跨 2
    if maxCount == 2 && n >= 6 && n%2 == 0 {
        if isConsecutive(byCount(m, 2)) && len(byCount(m, 2)) >= 3 {
            return TypePairStraight, byCount(m, 2)[0]
        }
    }
    // 飞机:连续三张(2 组起),带单/带对/不带
    if triples := byCount(m, 3); len(triples) >= 2 && isConsecutive(triples) {
        singles := byCount(m, 1)
        pairs := byCount(m, 2)
        if len(singles) == len(triples) && n == len(triples)*4 {
            return TypeAirplaneSingle, triples[0]
        }
        if len(pairs) == len(triples) && n == len(triples)*5 {
            return TypeAirplanePair, triples[0]
        }
        if n == len(triples)*3 {
            return TypeAirplane, triples[0]
        }
    }
    return TypeInvalid, 0
}

// isConsecutive 是否连续递增且不跨 2(2 是斗地主最大单牌,不参与顺子/连对/飞机)
func isConsecutive(rs []Rank) bool {
    for i := 1; i < len(rs); i++ {
        if rs[i] != rs[i-1]+1 || rs[i] > 15 {
            return false
        }
    }
    return true
}

// onlyRank 单牌值提取(m 里唯一 key)
func onlyRank(m map[Rank]int) Rank {
    for r := range m {
        return r
    }
    return 0
}
```

### 2.3 比较规则

```go
// Compare 比较两手同类型牌,返回 >0 / <0 / 0
// 前提:先比 HandType 等级(火箭 > 炸弹 > 普通);炸弹可压任意普通牌型
func Compare(a, b HandType, ar, br Rank) int {
    if a == TypeRocket {
        return 1
    }
    if b == TypeRocket {
        return -1
    }
    if a == TypeBomb && b != TypeBomb {
        return 1
    }
    if b == TypeBomb && a != TypeBomb {
        return -1
    }
    if a != b {
        return 0 // 类型不同且无炸弹,不能互相压
    }
    if ar > br {
        return 1
    }
    if ar < br {
        return -1
    }
    return 0
}
```

### 2.4 常见差异点核对清单（生成代码时逐条对照策划案）

- 顺子是否允许 `A2345`（部分地方玩法允许 A 作 1）；
- 三带二是否可带王对；
- 飞机带牌是否允许带炸弹作翅膀；
- 是否支持"四带二"（4 张带 2 单/2 对）；
- 春天/反春天、明牌、抢地主等流程规则的触发时机与倍数叠加顺序。

---

## 三、麻将胡牌检测参考实现

标准型胡牌：`3n+2`（n 个面子 + 1 对将），面子为顺子或刻子。

```go
package rules

// Tile 麻将牌:0-8 万,10-18 条,20-28 筒,30+ 字牌;用 int 便于顺子判定
type Tile int

// CanWin 判断 14 张牌是否胡牌(标准型 3n+2)
// tiles 必须先排序;递归回溯:先定将,再剥面子
func CanWin(tiles []Tile) bool {
    if len(tiles)%3 != 2 {
        return false
    }
    for i := 0; i < len(tiles)-1; i++ {
        if tiles[i] == tiles[i+1] {
            // 假设 i,i+1 是将牌
            rest := append(append([]Tile{}, tiles[:i]...), tiles[i+2:]...)
            if allMelds(rest) {
                return true
            }
        }
        // 跳过连续相同牌,避免重复试探同一将牌
        for i < len(tiles)-2 && tiles[i] == tiles[i+1] {
            i++
        }
    }
    return false
}

// allMelds 剩余牌能否全部拆成面子(顺子/刻子)
func allMelds(tiles []Tile) bool {
    if len(tiles) == 0 {
        return true
    }
    // 取最小牌,尝试三种拆法
    first := tiles[0]
    if countOf(tiles, first) >= 3 {
        if allMelds(removeN(tiles, first, 3)) {
            return true
        }
    }
    if first < 30 && // 字牌无顺子
        contains(tiles, first+1) && contains(tiles, first+2) {
        rest := removeN(tiles, first, 1)
        rest = removeN(rest, first+1, 1)
        rest = removeN(rest, first+2, 1)
        if allMelds(rest) {
            return true
        }
    }
    return false
}
```

辅助函数 `countOf` / `removeN` / `contains` 是对已排序切片的线性实现（从头部数 N 个相同牌值移除），生成代码时自行补齐，注意 `removeN` 必须保持排序不变。

### 麻将差异点核对清单

- 七对、十三幺等特殊牌型是否计入（策划案常单独定义）；
- 番数体系（门清、自摸、杠上开花等）是独立的计番模块，与胡牌检测解耦；
- 杠的处理时机（补牌后仍须满足牌数守恒：`3n+2` 中 n 含杠）。

---

## 四、测试向量策略（self_test 阶段直接复用）

棋牌规则 bug 的 90% 出在边界，必须**全枚举 + 表驱动**双管齐下：

### 4.1 表驱动用例（规则正确性）

```go
func TestParseHandType(t *testing.T) {
    cases := []struct {
        name string
        hand []Rank
        want HandType
        wantRank Rank
    }{
        {"单张", []Rank{5}, TypeSingle, 5},
        {"对子", []Rank{10, 10}, TypePair, 10},
        {"三带一", []Rank{3, 3, 3, 9}, TypeTripleOne, 3},
        {"顺子", []Rank{3, 4, 5, 6, 7}, TypeStraight, 3},
        {"顺子不跨2", []Rank{13, 14, 15, 16}, TypeInvalid, 0}, // K A 2 王不是顺子
        {"连对", []Rank{7, 7, 8, 8, 9, 9}, TypePairStraight, 7},
        {"炸弹", []Rank{15, 15, 15, 15}, TypeBomb, 15},
        {"王炸", []Rank{16, 17}, TypeRocket, 17},
        {"飞机带单", []Rank{3, 3, 3, 4, 4, 4, 8, 9}, TypeAirplaneSingle, 3},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            ht, r := Parse(c.hand)
            if ht != c.want || r != c.wantRank {
                t.Fatalf("Parse(%v) = (%v, %v), want (%v, %v)", c.hand, ht, r, c.want, c.wantRank)
            }
        })
    }
}
```

### 4.2 全枚举组合测试（隐藏 bug 挖掘）

斗地主单牌只有 15 种值，两两组合、三张组合可**完全枚举**，比对实现与朴素暴力判定结果：

```go
func TestAllPairsTriples(t *testing.T) {
    all := []Rank{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
    for _, a := range all {
        for _, b := range all {
            got, _ := Parse([]Rank{a, b})
            want := naiveClassify([]Rank{a, b}) // 朴素判定:张数+是否同值
            if got != want {
                t.Fatalf("Parse([%d %d]) = %v, want %v", a, b, got, want)
            }
        }
    }
}
```

### 4.3 属性测试（随机 10000 手）

- **不变量 1**：任意合法手牌 `Parse` 后重排仍得同一牌型（幂等性）；
- **不变量 2**：`Parse` 非 Invalid 时，该手牌按牌型定义的张数规则必须成立（如顺子必然无重复值）；
- **不变量 3**：随机发牌 10000 次，统计王炸出现频率应落在理论值 `2/C(54,2)` 的 ±20% 区间。

---

## 五、与 simulate_match 的衔接（self_test 阶段）

对局模拟器 `simulation/main.go` 的 JSON 报告必须包含：

1. **`probability_checks`**：从策划案提取全部概率/数值配置（发牌概率、特殊牌型出现率、抽奖权重），用本 skill 第 4.3 节的属性测试方法统计实测值，`expected` 填策划案值，`tolerance_pct` 建议 ≤10（概率越小容差可放宽，如 0.1% 概率事件容差 30）；
2. **`coin_conservation`**：模拟全程记录「系统产出金币 − 系统回收金币」，误差超过 0.5% 即说明存在结算/退金/奖励发放漏洞，必须先修复再跑模拟；
3. **局数 ≥ 1000**：低于此数统计波动会误报概率偏差。

正确示例（simulation/main.go 的最终输出）：

```json
{
  "matches": 10000,
  "coin_conservation": {"error_pct": 0.02},
  "probability_checks": [
    {"name": "王炸出现概率", "expected": 0.07, "actual": 0.068, "tolerance_pct": 30, "pass": true},
    {"name": "单局炸弹期望个数", "expected": 1.5, "actual": 1.47, "tolerance_pct": 10, "pass": true}
  ]
}
```
