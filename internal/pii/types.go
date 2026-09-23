// Package pii содержит движок обнаружения персональных данных:
// токенизацию текста, детекторы по типам, контекстные правила и разрешение
// пересечений найденных фрагментов.
package pii

// Type — тип персональных данных. Значения совпадают с именами типов в
// конфигурации и в метриках, поэтому менять их нельзя без правки конфигов.
type Type string

// Типы из раздела 4.1 технического задания.
const (
	TypeFIO           Type = "FIO"
	TypeDOB           Type = "DOB"
	TypeBirthPlace    Type = "BIRTH_PLACE"
	TypePassport      Type = "PASSPORT"
	TypeCitizenship   Type = "CITIZENSHIP"
	TypeIssuer        Type = "ISSUER"
	TypeDeptCode      Type = "DEPT_CODE"
	TypeIssueDate     Type = "ISSUE_DATE"
	TypeDriverLicense Type = "DRIVER_LICENSE"
	TypeAddress       Type = "ADDRESS"
	TypePostcode      Type = "POSTCODE"
	TypeEmail         Type = "EMAIL"
	TypePhone         Type = "PHONE"
	TypeINN           Type = "INN"
	TypeCard          Type = "CARD"
	TypeCVV           Type = "CVV"
	TypePIN           Type = "PIN"
	TypeCardHolder    Type = "CARDHOLDER"
)

// Дополнительные типы: документы, удостоверяющие личность, кроме паспорта РФ.
// Закрывают пункт 6 технического задания про расширенные сценарии.
const (
	TypeSNILS           Type = "SNILS"
	TypeForeignPassport Type = "FOREIGN_PASSPORT"
	TypeResidencePermit Type = "RESIDENCE_PERMIT"
	TypeBirthCert       Type = "BIRTH_CERT"
	TypeMilitaryID      Type = "MILITARY_ID"
)

// Типы, расширяющие перечень идентифицируемых персональных данных: банковские
// реквизиты, документы и идентификаторы, которые постоянно встречаются в
// банковских анкетах. Каждый тип добавляется детектором с якорями и формой,
// поэтому ядро при этом не меняется.
const (
	TypeAccount   Type = "ACCOUNT"
	TypeOMS       Type = "OMS"
	TypePlate     Type = "PLATE"
	TypeVIN       Type = "VIN"
	TypeIPAddress Type = "IP_ADDRESS"
)

// AllTypes перечисляет обязательные типы из технического задания в порядке,
// в котором их удобно показывать в отчётах о качестве.
func AllTypes() []Type {
	return []Type{
		TypeFIO, TypeDOB, TypeBirthPlace, TypePassport, TypeCitizenship,
		TypeIssuer, TypeDeptCode, TypeIssueDate, TypeDriverLicense, TypeAddress,
		TypePostcode, TypeEmail, TypePhone, TypeINN, TypeCard, TypeCVV,
		TypePIN, TypeCardHolder,
		TypeAccount, TypeOMS, TypePlate, TypeVIN, TypeIPAddress,
	}
}

// Уровни уверенности. Детектор, нашедший значение по якорю и по форме,
// обязан отдавать ConfAnchored даже если контрольная сумма не сошлась:
// жюри и проверяющая система подают выдуманные номера.
const (
	ConfLow      = 0.4
	ConfMedium   = 0.6
	ConfAnchored = 0.75
	ConfHigh     = 0.9
	ConfCertain  = 1.0
)

// Span — найденный фрагмент персональных данных в координатах байтов
// исходного текста. End не включается в диапазон.
type Span struct {
	Start  int
	End    int
	Type   Type
	Conf   float64
	Reason string
}

// Len возвращает длину фрагмента в байтах.
func (s Span) Len() int { return s.End - s.Start }

// Valid сообщает, что границы фрагмента непустые и не перевёрнуты.
func (s Span) Valid() bool { return s.Start >= 0 && s.End > s.Start }

// Overlaps сообщает, пересекается ли фрагмент с другим.
func (s Span) Overlaps(o Span) bool { return s.Start < o.End && o.Start < s.End }

// Detector находит фрагменты одного или нескольких типов персональных данных.
// Реализация обязана быть без состояния и безопасной для параллельного вызова:
// один экземпляр детектора обслуживает все запросы сервиса.
type Detector interface {
	// Types перечисляет типы, которые может вернуть детектор.
	Types() []Type
	// Detect ищет фрагменты в документе. Возвращаемые границы — байтовые
	// смещения в d.Text. Детектор не изменяет документ.
	Detect(d *Doc) []Span
}
