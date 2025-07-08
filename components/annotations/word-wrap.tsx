import {
  AnnotationHandler,
  InnerLine,
  InnerPre,
  InnerToken,
} from "codehike/code"

export const wordWrap: AnnotationHandler = {
  name: "word-wrap",
  Pre: (props) => <InnerPre merge={props} className="whitespace-pre-wrap" />,
  Line: (props) => (
    <InnerLine
      merge={props}
      style={{
        textIndent: `${-props.indentation}ch`,
        marginLeft: `${props.indentation}ch`,
      }}
    />
  ),
  Token: (props) => <InnerToken merge={props} style={{ textIndent: 0 }} />,
}
